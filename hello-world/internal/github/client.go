package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v57/github"
)

type Client struct {
	client *github.Client
	token  string
}

func NewClient(token string) *Client {
	// Create an HTTP client with authentication header
	httpClient := &http.Client{
		Transport: &authTransport{
			token: token,
			base:  http.DefaultTransport,
		},
	}

	return &Client{
		client: github.NewClient(httpClient),
		token:  token,
	}
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// Example: https://github.com/owner/repo -> (owner, repo, nil)
func ParseRepoURL(repoURL string) (string, string, error) {
	// Remove trailing slashes
	repoURL = strings.TrimSuffix(repoURL, "/")

	// Handle both https://github.com/owner/repo and github.com/owner/repo
	repoURL = strings.TrimPrefix(repoURL, "https://")
	repoURL = strings.TrimPrefix(repoURL, "http://")
	repoURL = strings.TrimPrefix(repoURL, "github.com/")

	parts := strings.Split(repoURL, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid GitHub URL format")
	}

	return parts[0], parts[1], nil
}

// Reuses existing fork if present to avoid creating duplicates
func (c *Client) ForkRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	// Try to get authenticated user first
	user, _, err := c.client.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated user: %w", err)
	}

	// Check if fork already exists
	existingFork, resp, err := c.client.Repositories.Get(ctx, user.GetLogin(), repo)
	if err == nil && existingFork != nil && existingFork.GetFork() {
		// Fork exists, return it
		return existingFork, nil
	}

	// If we get a 404, fork doesn't exist, so create it
	if resp != nil && resp.StatusCode == 404 {
		fork, _, err := c.client.Repositories.CreateFork(ctx, owner, repo, &github.RepositoryCreateForkOptions{
			DefaultBranchOnly: true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create fork: %w", err)
		}
		return fork, nil
	}

	// Some other error occurred
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing fork: %w", err)
	}

	// Fork exists but we couldn't determine it's a fork, return it anyway
	return existingFork, nil
}

func (c *Client) GetAuthenticatedUser(ctx context.Context) (*github.User, error) {
	user, _, err := c.client.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated user: %w", err)
	}

	return user, nil
}

func (c *Client) CreatePullRequest(ctx context.Context, upstreamOwner, upstreamRepo, forkOwner, title, body, headBranch, baseBranch string) (*github.PullRequest, error) {
	// The head should be in format "forkOwner:branch"
	head := fmt.Sprintf("%s:%s", forkOwner, headBranch)

	newPR := &github.NewPullRequest{
		Title: github.String(title),
		Body:  github.String(body),
		Head:  github.String(head),
		Base:  github.String(baseBranch),
	}

	pr, _, err := c.client.PullRequests.Create(ctx, upstreamOwner, upstreamRepo, newPR)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	return pr, nil
}

func (c *Client) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	repository, _, err := c.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("failed to get repository: %w", err)
	}

	return repository.GetDefaultBranch(), nil
}

// ListOpenPullRequests lists open pull requests from a specific head (fork owner:branch)
func (c *Client) ListOpenPullRequests(ctx context.Context, upstreamOwner, upstreamRepo, forkOwner, headBranch string) ([]*github.PullRequest, error) {
	head := fmt.Sprintf("%s:%s", forkOwner, headBranch)

	opts := &github.PullRequestListOptions{
		State: "open",
		Head:  head,
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	prs, _, err := c.client.PullRequests.List(ctx, upstreamOwner, upstreamRepo, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list pull requests: %w", err)
	}

	return prs, nil
}

func (c *Client) ClosePullRequest(ctx context.Context, owner, repo string, prNumber int, comment string) error {
	// Add a comment explaining the closure
	if comment != "" {
		prComment := &github.IssueComment{
			Body: github.String(comment),
		}
		_, _, err := c.client.Issues.CreateComment(ctx, owner, repo, prNumber, prComment)
		if err != nil {
			return fmt.Errorf("failed to add comment: %w", err)
		}
	}

	// Close the PR
	state := "closed"
	prUpdate := &github.PullRequest{
		State: &state,
	}

	_, _, err := c.client.PullRequests.Edit(ctx, owner, repo, prNumber, prUpdate)
	if err != nil {
		return fmt.Errorf("failed to close pull request: %w", err)
	}

	return nil
}

func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	ref := fmt.Sprintf("heads/%s", branch)
	_, err := c.client.Git.DeleteRef(ctx, owner, repo, ref)
	if err != nil {
		return fmt.Errorf("failed to delete branch: %w", err)
	}
	return nil
}

// Grants "push" permission, allowing user to edit PR branches directly
func (c *Client) AddCollaborator(ctx context.Context, owner, repo, username string) error {
	// Use "push" permission to give write access
	opts := &github.RepositoryAddCollaboratorOptions{
		Permission: "push",
	}

	_, _, err := c.client.Repositories.AddCollaborator(ctx, owner, repo, username, opts)
	if err != nil {
		return fmt.Errorf("failed to add collaborator: %w", err)
	}

	return nil
}
