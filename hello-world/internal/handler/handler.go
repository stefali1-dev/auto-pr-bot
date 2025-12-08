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
	"hello-world/internal/ratelimit"
	"hello-world/internal/status"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/google/uuid"
)

type Handler struct {
	githubClient  *github.Client
	openaiClient  *openai.Client
	githubToken   string
	statusTracker *status.Tracker
	rateLimiter   *ratelimit.Limiter
}

func New() (*Handler, error) {
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	openaiClient, err := openai.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenAI client: %w", err)
	}

	statusTracker, err := status.NewTracker(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to create status tracker: %w", err)
	}

	rateLimiter, err := ratelimit.NewLimiter(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}

	return &Handler{
		githubClient:  github.NewClient(githubToken),
		openaiClient:  openaiClient,
		githubToken:   githubToken,
		statusTracker: statusTracker,
		rateLimiter:   rateLimiter,
	}, nil
}

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

		// Get IP address from request
		ipAddress := request.RequestContext.Identity.SourceIP
		if ipAddress == "" {
			ipAddress = "unknown"
		}
		log.Printf("Request from IP: %s", ipAddress)

		// Check rate limit
		rateLimitResult, err := h.rateLimiter.CheckRateLimit(ctx, ipAddress)
		if err != nil {
			log.Printf("Warning: Failed to check rate limit: %v", err)
			// Continue processing even if rate limit check fails
		} else if !rateLimitResult.Allowed {
			log.Printf("Rate limit exceeded for IP %s: %d/%d requests used", ipAddress, rateLimitResult.RequestsUsed, rateLimitResult.RequestsLimit)
			return h.rateLimitErrorResponse(rateLimitResult)
		}

		// Generate unique request ID
		requestID := uuid.New().String()
		log.Printf("Generated request ID: %s", requestID)

		// Record this request for rate limiting
		if err := h.rateLimiter.RecordRequest(ctx, ipAddress, requestID); err != nil {
			log.Printf("Warning: Failed to record rate limit: %v", err)
		}

		// Create initial status record
		if err = h.statusTracker.Update(ctx, requestID, status.StatusPending, "Request received, starting processing...", 0, req.RepositoryURL); err != nil {
			log.Printf("Warning: Failed to create initial status: %v", err)
		}

		// Add requestID to the request for async processing
		reqWithID := models.RequestWithID{
			Request:   req,
			RequestID: requestID,
		}

		requestBodyWithID, err := json.Marshal(reqWithID)
		if err != nil {
			log.Printf("Failed to marshal request with ID: %v", err)
			return h.errorResponse(500, "Failed to start processing")
		}

		// Invoke this Lambda function asynchronously
		if err := h.invokeAsync(ctx, string(requestBodyWithID)); err != nil {
			log.Printf("Failed to invoke async: %v", err)

			// Check if it's a concurrency limit error
			if strings.Contains(err.Error(), "ReservedConcurrentExecutions") ||
				strings.Contains(err.Error(), "TooManyRequestsException") ||
				strings.Contains(err.Error(), "Rate exceeded") {
				log.Printf("Concurrency limit reached")
				return h.errorResponse(503, "Bot is currently at capacity processing other requests. Please try again in a few minutes.")
			}

			h.statusTracker.Error(ctx, requestID, fmt.Sprintf("Failed to start async processing: %v", err), req.RepositoryURL)
			return h.errorResponse(500, fmt.Sprintf("Failed to start processing: %v", err))
		}

		// Return 202 Accepted immediately with requestId
		responseBody := fmt.Sprintf(`{"status":"processing","message":"Your request is being processed.","repository":"%s","requestId":"%s"}`, req.RepositoryURL, requestID)
		return events.APIGatewayProxyResponse{
			StatusCode: 202,
			Headers: map[string]string{
				"Content-Type":                 "application/json",
				"Access-Control-Allow-Origin":  "*",
				"Access-Control-Allow-Methods": "POST, OPTIONS",
				"Access-Control-Allow-Headers": "Content-Type",
			},
			Body: responseBody,
		}, nil
	}

	// This is an async invocation - do the actual processing
	log.Printf("Asynchronous invocation detected - processing repository")

	// Parse the request to extract requestID
	var reqWithID models.RequestWithID
	if err := json.Unmarshal([]byte(request.Body), &reqWithID); err != nil {
		log.Printf("ERROR: Failed to parse request with ID: %v", err)
		return events.APIGatewayProxyResponse{}, fmt.Errorf("failed to parse request: %w", err)
	}

	requestID := reqWithID.RequestID
	if requestID == "" {
		log.Printf("Warning: No requestID found in async invocation")
		requestID = uuid.New().String()
	}

	result, err := h.processRepository(ctx, &reqWithID.Request, requestID)
	if err != nil {
		log.Printf("ERROR: Failed to process repository: %v", err)
		// Don't overwrite rejected status - it's already set with helpful feedback
		// Only update to error status if it's not a validation rejection
		if !strings.Contains(err.Error(), "prompt validation failed") {
			h.statusTracker.Error(ctx, requestID, err.Error(), reqWithID.RepositoryURL)
		}
		return events.APIGatewayProxyResponse{}, fmt.Errorf("failed to process repository: %w", err)
	}

	log.Printf("SUCCESS: %s", result)
	return events.APIGatewayProxyResponse{}, nil
}

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

func (h *Handler) processRepository(ctx context.Context, req *models.Request, requestID string) (string, error) {
	// Parse repository URL
	owner, repo, err := github.ParseRepoURL(req.RepositoryURL)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL: %w", err)
	}

	log.Printf("Parsed repository: owner=%s, repo=%s", owner, repo)

	// Step 0: Validate the modification prompt
	h.statusTracker.Update(ctx, requestID, status.StatusValidating, "Validating modification request...", 0, req.RepositoryURL)
	log.Printf("Validating modification prompt...")
	isValid, reason, err := h.openaiClient.ValidatePrompt(ctx, req.ModificationPrompt)
	if err != nil {
		log.Printf("Warning: Failed to validate prompt: %v. Continuing anyway.", err)
		// Don't fail the entire process if validation fails - continue with the request
	} else if !isValid {
		log.Printf("Prompt validation failed: %s", reason)
		h.statusTracker.Reject(ctx, requestID, reason, req.RepositoryURL)
		return "", fmt.Errorf("prompt validation failed: %s", reason)
	} else {
		log.Printf("Prompt validation passed: %s", reason)
	}

	// Step 1: Fork the repository
	h.statusTracker.Update(ctx, requestID, status.StatusForking, "Forking repository...", 1, req.RepositoryURL)
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

	// Step 2: Clone the forked repository
	h.statusTracker.Update(ctx, requestID, status.StatusCloning, "Cloning forked repository...", 2, req.RepositoryURL)
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

	// Step 3: Call OpenAI to analyze which files to read
	h.statusTracker.Update(ctx, requestID, status.StatusAnalyzing, "Analyzing repository with AI...", 3, req.RepositoryURL)
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
	h.statusTracker.Update(ctx, requestID, status.StatusModifying, "Generating code modifications with AI...", 4, req.RepositoryURL)
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
	h.statusTracker.Update(ctx, requestID, status.StatusCommitting, "Committing and pushing changes...", 5, req.RepositoryURL)
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
	h.statusTracker.Update(ctx, requestID, status.StatusCreatingPR, "Creating pull request...", 6, req.RepositoryURL)
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

	// Mark as completed in status tracker
	h.statusTracker.Complete(ctx, requestID, pr.GetHTMLURL(), req.RepositoryURL)

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

// POSIX standard requires text files to end with a newline
func ensureTrailingNewline(content string) string {
	if content == "" {
		return content
	}
	if !strings.HasSuffix(content, "\n") {
		return content + "\n"
	}
	return content
}

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

func formatModifiedFilesList(modified map[string]string) string {
	var builder strings.Builder
	for file := range modified {
		builder.WriteString(fmt.Sprintf("- %s\n", file))
	}
	return builder.String()
}

func (h *Handler) successResponse(message string) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: message,
	}, nil
}

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

func (h *Handler) rateLimitErrorResponse(rateLimitResult *ratelimit.RateLimitResult) (events.APIGatewayProxyResponse, error) {
	rateLimitError := models.RateLimitError{
		Error: "Rate limit exceeded",
		RateLimit: models.RateLimitInfo{
			Limit:      rateLimitResult.RequestsLimit,
			Used:       rateLimitResult.RequestsUsed,
			ResetAt:    rateLimitResult.NextAvailable.Unix(),
			ResetAtISO: rateLimitResult.NextAvailable.Format(time.RFC3339),
		},
	}

	body, err := json.Marshal(rateLimitError)
	if err != nil {
		log.Printf("Failed to marshal rate limit error: %v", err)
		return h.errorResponse(429, "Rate limit exceeded")
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 429,
		Headers: map[string]string{
			"Content-Type":                 "application/json",
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": "POST, OPTIONS",
			"Access-Control-Allow-Headers": "Content-Type",
		},
		Body: string(body),
	}, nil
}
