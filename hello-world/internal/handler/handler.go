package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"hello-world/internal/git"
	"hello-world/internal/github"
	"hello-world/internal/models"
	"hello-world/internal/openai"

	"github.com/aws/aws-lambda-go/events"
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

	// Process the repository (no retry at this level - retries are handled in OpenAI client)
	result, err := h.processRepository(ctx, &req)
	if err != nil {
		return h.errorResponse(500, fmt.Sprintf("Failed to process repository: %v", err))
	}

	return h.successResponse(result)
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

	// Step 6: Commit and push changes
	log.Printf("Step 6: Committing and pushing changes...")
	commitMessage := fmt.Sprintf("Auto PR: %s\n\n%s", req.ModificationPrompt, explanation)
	err = git.CommitAndPush(clonePath, commitMessage, h.githubToken)

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
		log.Printf("Changes committed and pushed successfully")
	}

	// Step 7: Get the default branch of the upstream repository
	log.Printf("Step 7: Getting default branch of upstream repository...")
	defaultBranch, err := h.githubClient.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("failed to get default branch: %w", err)
	}
	log.Printf("Default branch: %s", defaultBranch)

	// Step 8: Check for and close existing PRs from this bot
	log.Printf("Step 8: Checking for existing PRs from bot...")
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

		// Close existing PRs since we have new changes
		log.Printf("Found %d existing PR(s) from bot, closing them...", len(existingPRs))
		for _, existingPR := range existingPRs {
			closeComment := fmt.Sprintf("Closing this PR to create a new one with updated changes.\n\nNew modification request: %s", req.ModificationPrompt)
			if err := h.githubClient.ClosePullRequest(ctx, owner, repo, existingPR.GetNumber(), closeComment); err != nil {
				log.Printf("Warning: failed to close PR #%d: %v", existingPR.GetNumber(), err)
			} else {
				log.Printf("Closed PR #%d", existingPR.GetNumber())
			}
		}
	} else if !hasChanges {
		// No existing PRs and no changes - this shouldn't happen but handle it gracefully
		return "", fmt.Errorf("no changes to commit and no existing PR found")
	}

	// Step 9: Create Pull Request
	log.Printf("Step 9: Creating pull request...")
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
		defaultBranch, // head branch (from fork)
		defaultBranch, // base branch (to upstream)
	)
	if err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	log.Printf("Pull request created: %s", pr.GetHTMLURL())

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
			"Content-Type": "application/json",
		},
		Body: fmt.Sprintf(`{"error": "%s"}`, message),
	}, nil
}
