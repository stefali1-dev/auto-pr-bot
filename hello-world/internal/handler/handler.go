package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"hello-world/internal/git"
	"hello-world/internal/github"
	"hello-world/internal/models"

	"github.com/aws/aws-lambda-go/events"
)

const (
	maxRetries = 2
)

// Handler processes the Lambda request
type Handler struct {
	githubClient *github.Client
	githubToken  string
}

// New creates a new handler instance
func New() (*Handler, error) {
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	return &Handler{
		githubClient: github.NewClient(githubToken),
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

	// Execute with retries
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("Retry attempt %d/%d", attempt, maxRetries)
		}

		result, err := h.processRepository(ctx, &req)
		if err == nil {
			return h.successResponse(result)
		}

		lastErr = err
		log.Printf("Attempt %d failed: %v", attempt+1, err)
	}

	return h.errorResponse(500, fmt.Sprintf("Failed after %d retries: %v", maxRetries+1, lastErr))
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

	// Prepare response
	response := fmt.Sprintf(
		"Repository processed successfully!\n\n"+
			"Original: %s/%s\n"+
			"Fork: %s\n"+
			"Cloned to: %s\n\n"+
			"File structure:\n%s\n\n"+
			"Modification prompt: %s\n"+
			"Requesting user: %s",
		owner, repo,
		fork.GetHTMLURL(),
		clonePath,
		fileTree,
		req.ModificationPrompt,
		req.GitHubUsername,
	)

	return response, nil
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
