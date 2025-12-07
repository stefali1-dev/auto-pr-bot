package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"hello-world/internal/git"
	"hello-world/internal/github"
	"hello-world/internal/models"
	"hello-world/internal/openai"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// Handler processes the Lambda request
type Handler struct {
	githubClient *github.Client
	openaiClient *openai.Client
	githubToken  string
}

// New creates a new handler instance
func New() (*Handler, error) {
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	openaiClient, err := openai.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenAI client: %w", err)
	}

	return &Handler{
		githubClient: github.NewClient(githubToken),
		openaiClient: openaiClient,
		githubToken:  githubToken,
	}, nil
}

// Handle processes the incoming API Gateway request
func (h *Handler) Handle(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("Received request: %s", request.Body)

	// Parse and validate request
	var req models.Request
	if err := json.Unmarshal([]byte(request.Body), &req); err != nil {
		return h.errorResponse(400, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if err := req.Validate(); err != nil {
		return h.errorResponse(400, err.Error())
	}

	log.Printf("Processing request for repository: %s, user: %s", req.RepositoryURL, req.GitHubUsername)

	// Check if this is a synchronous call from API Gateway
	// API Gateway requests have RequestContext with a RequestId
	// Async invocations will have empty RequestContext
	if request.RequestContext.RequestID != "" {
		log.Printf("Synchronous invocation detected - invoking async and returning immediately")

		// Invoke this Lambda function asynchronously
		if err := h.invokeAsync(ctx, request.Body); err != nil {
			log.Printf("Failed to invoke async: %v", err)
			return h.errorResponse(500, fmt.Sprintf("Failed to start processing: %v", err))
		}

		// Return 202 Accepted immediately
		return events.APIGatewayProxyResponse{
			StatusCode: 202,
			Headers: map[string]string{
				"Content-Type":                 "application/json",
				"Access-Control-Allow-Origin":  "*",
				"Access-Control-Allow-Methods": "POST, OPTIONS",
				"Access-Control-Allow-Headers": "Content-Type",
			},
			Body: `{"status":"processing","message":"Your request is being processed. Check CloudWatch logs for progress.","repository":"` + req.RepositoryURL + `"}`,
		}, nil
	}

	// This is an async invocation - do the actual processing
	log.Printf("Asynchronous invocation detected - processing repository")
	result, err := h.processRepository(ctx, &req)
	if err != nil {
		log.Printf("ERROR: Failed to process repository: %v", err)
		return events.APIGatewayProxyResponse{}, fmt.Errorf("failed to process repository: %w", err)
	}

	log.Printf("SUCCESS: %s", result)
	return events.APIGatewayProxyResponse{}, nil
}

// invokeAsync invokes this Lambda function asynchronously
func (h *Handler) invokeAsync(ctx context.Context, payload string) error {
	functionName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	if functionName == "" {
		return fmt.Errorf("AWS_LAMBDA_FUNCTION_NAME not set")
	}

	// Load AWS config
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create Lambda client
	lambdaClient := lambda.NewFromConfig(cfg)

	// Create API Gateway event with empty RequestContext to signal async processing
	asyncEvent := events.APIGatewayProxyRequest{
		Body:           payload,
		RequestContext: events.APIGatewayProxyRequestContext{}, // Empty context signals async
	}

	asyncPayload, err := json.Marshal(asyncEvent)
	if err != nil {
		return fmt.Errorf("failed to marshal async payload: %w", err)
	}

	// Invoke asynchronously
	input := &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: types.InvocationTypeEvent, // Event = async
		Payload:        asyncPayload,
	}

	_, err = lambdaClient.Invoke(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to invoke Lambda async: %w", err)
	}

	log.Printf("Successfully invoked Lambda asynchronously")
	return nil
}

// processRepository handles the main business logic
func (h *Handler) processRepository(ctx context.Context, req *models.Request) (string, error) {
	// Parse repository URL
	owner, repo, err := github.ParseRepoURL(req.RepositoryURL)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL: %w", err)
	}

	log.Printf("Parsed repository: owner=%s, repo=%s", owner, repo)

	// Fork the repository
	log.Printf("Forking repository %s/%s...", owner, repo)
	fork, err := h.githubClient.ForkRepository(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("fork failed: %w", err)
	}

	log.Printf("Fork created: %s", fork.GetHTMLURL())

	// Get authenticated user info
	user, err := h.githubClient.GetAuthenticatedUser(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get user info: %w", err)
	}

	log.Printf("Authenticated as: %s", user.GetLogin())

	// Clone the forked repository
	cloneOpts := git.CloneOptions{
		URL:       fork.GetCloneURL(),
		Directory: fmt.Sprintf("%s-%s", user.GetLogin(), repo),
		Token:     h.githubToken,
	}

	log.Printf("Cloning repository to /tmp...")
	clonePath, err := git.CloneRepository(cloneOpts)
	if err != nil {
		return "", fmt.Errorf("clone failed: %w", err)
	}

	// Ensure cleanup happens
	defer func() {
		log.Printf("Cleaning up repository at %s", clonePath)
		if cleanupErr := git.Cleanup(clonePath); cleanupErr != nil {
			log.Printf("Warning: cleanup failed: %v", cleanupErr)
		}
	}()

	log.Printf("Repository cloned to: %s", clonePath)

	// Get the default branch before making changes
	log.Printf("Getting default branch of upstream repository...")
	defaultBranch, err := h.githubClient.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("failed to get default branch: %w", err)
	}
	log.Printf("Default branch: %s", defaultBranch)

	// Reset fork's main branch to match upstream
	log.Printf("Resetting fork to match upstream...")
	if err := git.ResetToUpstream(clonePath, owner, repo, defaultBranch); err != nil {
		return "", fmt.Errorf("failed to reset to upstream: %w", err)
	}
	log.Printf("Fork reset to upstream successfully")

	// Create a new branch with timestamp
	branchName := fmt.Sprintf("auto-pr-bot/%d", time.Now().Unix())
	log.Printf("Creating new branch: %s", branchName)
	if err := git.CreateAndCheckoutBranch(clonePath, branchName); err != nil {
		return "", fmt.Errorf("failed to create branch: %w", err)
	}

	// List all files in the repository
	log.Printf("Listing files in repository...")
	fileTree, err := git.ListFiles(clonePath)
	if err != nil {
		return "", fmt.Errorf("failed to list files: %w", err)
	}

	log.Printf("Repository file structure:\n%s", fileTree)

	// Step 1: Call OpenAI to analyze which files to read
	log.Printf("Step 1: Calling OpenAI to determine which files to read...")
	history, filesToRead, err := h.openaiClient.AnalyzeRepositoryForFiles(ctx, fileTree, req.ModificationPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to analyze repository with OpenAI: %w", err)
	}

	log.Printf("Files to read: %v", filesToRead)

	// Step 2: Read the identified files
	log.Printf("Step 2: Reading file contents...")
	fileContents := make(map[string]string)
	for _, relPath := range filesToRead {
		fullPath := fmt.Sprintf("%s/%s", clonePath, relPath)
		content, err := git.ReadFileContent(fullPath)
		if err != nil {
			log.Printf("Warning: failed to read file %s: %v", relPath, err)
			continue
		}
		fileContents[relPath] = content
		log.Printf("Read file: %s (%d bytes)", relPath, len(content))
	}

	if len(fileContents) == 0 {
		return "", fmt.Errorf("no files could be read")
	}

	// Step 3: Call OpenAI to determine which files to modify
	log.Printf("Step 3: Calling OpenAI to determine which files to modify...")
	filesToModify, explanation, err := h.openaiClient.DetermineFilesToModify(ctx, history, fileContents, req.ModificationPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to determine files to modify: %w", err)
	}

	log.Printf("Files to modify: %v", filesToModify)
	log.Printf("Explanation: %s", explanation)

	// Step 4: Generate modified content for each file
	log.Printf("Step 4: Generating modified file contents...")
	modifiedFiles := make(map[string]string)
	for _, filePath := range filesToModify {
		originalContent, exists := fileContents[filePath]
		if !exists {
			log.Printf("Warning: file %s was not in the read list, attempting to read it now", filePath)
			fullPath := fmt.Sprintf("%s/%s", clonePath, filePath)
			content, err := git.ReadFileContent(fullPath)
			if err != nil {
				log.Printf("Warning: failed to read file %s: %v", filePath, err)
				continue
			}
			originalContent = content
		}

		log.Printf("Generating modifications for: %s", filePath)
		modifiedContent, err := h.openaiClient.GenerateModifiedFile(ctx, history, filePath, originalContent, req.ModificationPrompt)
		if err != nil {
			log.Printf("Warning: failed to generate modifications for %s: %v", filePath, err)
			continue
		}

		modifiedFiles[filePath] = modifiedContent
		log.Printf("Generated modifications for: %s (%d bytes)", filePath, len(modifiedContent))
	}

	if len(modifiedFiles) == 0 {
		return "", fmt.Errorf("no files could be modified")
	}

	// Step 5: Write modified files to disk
	log.Printf("Step 5: Writing modified files to disk...")
	for filePath, content := range modifiedFiles {
		fullPath := fmt.Sprintf("%s/%s", clonePath, filePath)
		// Ensure content ends with newline (POSIX standard)
		content = ensureTrailingNewline(content)
		if err := git.WriteFile(fullPath, content); err != nil {
			return "", fmt.Errorf("failed to write file %s: %w", filePath, err)
		}
		log.Printf("Wrote file: %s", filePath)
	}

	// Step 6: Commit and push changes to the new branch
	log.Printf("Step 6: Committing and pushing changes to branch %s...", branchName)
	commitMessage := fmt.Sprintf("Auto PR: %s\n\n%s", req.ModificationPrompt, explanation)
	err = git.CommitAndPush(clonePath, branchName, commitMessage, h.githubToken)

	// Check if there are no changes to commit
	hasChanges := true
	if err != nil {
		if strings.Contains(err.Error(), "no changes to commit") {
			log.Printf("No changes detected - files are already up to date")
			hasChanges = false
		} else {
			return "", fmt.Errorf("failed to commit and push: %w", err)
		}
	} else {
		log.Printf("Changes committed and pushed successfully to branch %s", branchName)
	}

	// Step 7: Check for and close existing PRs from the default branch
	// Note: We ONLY close PRs from the default branch. PRs from feature branches
	// (auto-pr-bot/<timestamp>) are left open, allowing multiple concurrent PRs per repo.
	// This gives users flexibility to work on multiple independent changes.
	log.Printf("Step 7: Checking for existing PRs from bot (default branch: %s)...", defaultBranch)
	existingPRs, err := h.githubClient.ListOpenPullRequests(ctx, owner, repo, user.GetLogin(), defaultBranch)
	if err != nil {
		log.Printf("Warning: failed to list existing PRs: %v", err)
	} else if len(existingPRs) > 0 {
		// If there are no new changes and PRs already exist, just return success
		if !hasChanges {
			log.Printf("Found existing PR(s) and no new changes - nothing to do")
			existingPR := existingPRs[0]
			response := fmt.Sprintf(
				"No changes needed - PR already exists!\n\n"+
					"Original: %s/%s\n"+
					"Fork: %s\n"+
					"Existing Pull Request: %s\n\n"+
					"The requested changes are already in the open PR.",
				owner, repo,
				fork.GetHTMLURL(),
				existingPR.GetHTMLURL(),
			)
			return response, nil
		}

		// Close existing default-branch PRs and delete their branches
		log.Printf("Found %d existing default-branch PR(s), closing them and deleting branches...", len(existingPRs))
		for _, existingPR := range existingPRs {
			oldBranch := existingPR.Head.GetRef()
			closeComment := fmt.Sprintf("Closing this PR to create a new one with updated changes.\n\nNew modification request: %s", req.ModificationPrompt)
			if err := h.githubClient.ClosePullRequest(ctx, owner, repo, existingPR.GetNumber(), closeComment); err != nil {
				log.Printf("Warning: failed to close PR #%d: %v", existingPR.GetNumber(), err)
			} else {
				log.Printf("Closed PR #%d", existingPR.GetNumber())
			}

			// Delete the old branch from fork (skip if it's the default branch)
			if oldBranch != defaultBranch {
				if err := h.githubClient.DeleteBranch(ctx, user.GetLogin(), repo, oldBranch); err != nil {
					log.Printf("Warning: failed to delete branch %s: %v", oldBranch, err)
				} else {
					log.Printf("Deleted branch %s", oldBranch)
				}
			} else {
				log.Printf("Skipping deletion of default branch %s", oldBranch)
			}
		}
	} else if !hasChanges {
		// No existing PRs and no changes - this shouldn't happen but handle it gracefully
		return "", fmt.Errorf("no changes to commit and no existing PR found")
	}

	// Step 8: Create Pull Request from the new branch
	log.Printf("Step 8: Creating pull request from branch %s...", branchName)
	prTitle := fmt.Sprintf("Auto PR: %s", req.ModificationPrompt)
	prBody := fmt.Sprintf(`This is an automated pull request.

**Modification Request:**
%s

**Changes Made:**
%s

**Modified Files:**
%s

---
*Generated by Auto PR Bot*`, req.ModificationPrompt, explanation, formatModifiedFilesList(modifiedFiles))

	pr, err := h.githubClient.CreatePullRequest(
		ctx,
		owner,           // upstream owner
		repo,            // upstream repo
		user.GetLogin(), // fork owner
		prTitle,
		prBody,
		branchName,    // head branch (the new timestamp branch)
		defaultBranch, // base branch (upstream's default branch)
	)
	if err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	log.Printf("Pull request created: %s", pr.GetHTMLURL())

	// Step 9: Add GitHub user as collaborator to the fork if provided
	if req.GitHubUsername != "" {
		log.Printf("Step 9: Adding %s as collaborator to fork %s/%s...", req.GitHubUsername, user.GetLogin(), repo)
		if err := h.githubClient.AddCollaborator(ctx, user.GetLogin(), repo, req.GitHubUsername); err != nil {
			log.Printf("Warning: failed to add collaborator %s: %v", req.GitHubUsername, err)
			log.Printf("The PR was created successfully, but the user may need to be added manually")
		} else {
			log.Printf("Successfully added %s as collaborator to fork - they have write access and can push to PR branches", req.GitHubUsername)
		}
	} else {
		log.Printf("No GitHub username provided - skipping collaborator assignment")
	}

	// Print summary to CloudWatch
	log.Printf("\n=== MODIFICATION SUMMARY ===")
	log.Printf("Repository: %s/%s", owner, repo)
	log.Printf("Fork: %s", fork.GetHTMLURL())
	log.Printf("Modification prompt: %s", req.ModificationPrompt)
	log.Printf("\nFiles analyzed: %d", len(filesToRead))
	for _, file := range filesToRead {
		log.Printf("  - %s", file)
	}
	log.Printf("\nFiles modified: %d", len(modifiedFiles))
	for file := range modifiedFiles {
		log.Printf("  - %s", file)
	}
	log.Printf("\nExplanation: %s", explanation)
	log.Printf("Pull Request: %s", pr.GetHTMLURL())
	log.Printf("=== END SUMMARY ===\n")

	// Prepare response
	response := fmt.Sprintf(
		"Repository processed successfully!\n\n"+
			"Original: %s/%s\n"+
			"Fork: %s\n"+
			"Pull Request: %s\n\n"+
			"Files analyzed: %d\n"+
			"Files modified: %d\n\n"+
			"Explanation: %s\n\n"+
			"Modified Files:\n%s",
		owner, repo,
		fork.GetHTMLURL(),
		pr.GetHTMLURL(),
		len(filesToRead),
		len(modifiedFiles),
		explanation,
		formatModifiedFilesList(modifiedFiles),
	)

	return response, nil
}

// ensureTrailingNewline ensures content ends with a newline character (POSIX standard)
func ensureTrailingNewline(content string) string {
	if content == "" {
		return content
	}
	if !strings.HasSuffix(content, "\n") {
		return content + "\n"
	}
	return content
}

// formatFileList creates a formatted string of files
func formatFileList(analyzed []string, modified map[string]string) string {
	var builder strings.Builder
	builder.WriteString("\nAnalyzed:\n")
	for _, file := range analyzed {
		builder.WriteString(fmt.Sprintf("  - %s\n", file))
	}
	builder.WriteString("\nTo be modified:\n")
	for file := range modified {
		builder.WriteString(fmt.Sprintf("  - %s\n", file))
	}
	return builder.String()
}

// formatModifiedFilesList creates a formatted string of modified files
func formatModifiedFilesList(modified map[string]string) string {
	var builder strings.Builder
	for file := range modified {
		builder.WriteString(fmt.Sprintf("- %s\n", file))
	}
	return builder.String()
}

// successResponse creates a successful API Gateway response
func (h *Handler) successResponse(message string) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: message,
	}, nil
}

// errorResponse creates an error API Gateway response
func (h *Handler) errorResponse(statusCode int, message string) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type":                 "application/json",
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": "POST, OPTIONS",
			"Access-Control-Allow-Headers": "Content-Type",
		},
		Body: fmt.Sprintf(`{"error": "%s"}`, message),
	}, nil
}
