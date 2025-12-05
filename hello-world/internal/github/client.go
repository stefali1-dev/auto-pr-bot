package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v57/github"
)

// Client wraps the GitHub API client
type Client struct {
	client *github.Client
	token  string
}

// NewClient creates a new GitHub client with authentication
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

// authTransport adds the GitHub token to each request
type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
} // ParseRepoURL extracts owner and repo name from GitHub URL
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

// ForkRepository creates a fork of the specified repository or returns existing fork
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

// GetAuthenticatedUser returns the currently authenticated user
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*github.User, error) {
	user, _, err := c.client.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated user: %w", err)
	}

	return user, nil
}
