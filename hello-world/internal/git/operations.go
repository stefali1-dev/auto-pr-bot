package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	tmpDir = "/tmp"
)

// CloneOptions contains options for cloning a repository
type CloneOptions struct {
	URL       string
	Directory string
	Token     string
}

// CloneRepository clones a repository to the specified directory in /tmp
func CloneRepository(opts CloneOptions) (string, error) {
	// Create the full path in /tmp
	clonePath := filepath.Join(tmpDir, opts.Directory)

	// Clean up if directory already exists
	if err := os.RemoveAll(clonePath); err != nil {
		return "", fmt.Errorf("failed to clean up existing directory: %w", err)
	}

	// Construct clone URL with authentication
	authURL := strings.Replace(opts.URL, "https://", fmt.Sprintf("https://%s@", opts.Token), 1)

	// Execute git clone command
	cmd := exec.Command("git", "clone", "--depth", "1", authURL, clonePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}

	return clonePath, nil
}

// ListFiles recursively lists all files in a directory, returning a tree structure
func ListFiles(rootPath string) (string, error) {
	var builder strings.Builder

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip .git directory
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		// Calculate relative path and indentation
		relPath, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		// Calculate depth for indentation
		depth := strings.Count(relPath, string(os.PathSeparator))
		indent := strings.Repeat("  ", depth)

		// Add directory or file marker
		if info.IsDir() {
			builder.WriteString(fmt.Sprintf("%s%s/\n", indent, info.Name()))
		} else {
			builder.WriteString(fmt.Sprintf("%s%s\n", indent, info.Name()))
		}

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to walk directory: %w", err)
	}

	return builder.String(), nil
}

// ReadFileContent reads a file and returns its content
// For large files (>2000 lines), it returns the first 1000 and last 1000 lines with a truncation notice
func ReadFileContent(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	totalLines := len(lines)

	// If file is small enough, return it as-is
	if totalLines <= 2000 {
		return string(content), nil
	}

	// For large files, truncate
	first1000 := strings.Join(lines[:1000], "\n")
	last1000 := strings.Join(lines[totalLines-1000:], "\n")

	truncated := fmt.Sprintf("%s\n\n... [TRUNCATED: %d lines omitted] ...\n\n%s",
		first1000,
		totalLines-2000,
		last1000,
	)

	return truncated, nil
}

// WriteFile writes content to a file in the repository
func WriteFile(filePath, content string) error {
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

// ResetToUpstream resets the fork's main branch to match upstream
func ResetToUpstream(repoPath, upstreamOwner, upstreamRepo, defaultBranch string) error {
	// Add upstream remote if it doesn't exist
	remoteURL := fmt.Sprintf("https://github.com/%s/%s.git", upstreamOwner, upstreamRepo)
	addRemoteCmd := exec.Command("git", "-C", repoPath, "remote", "add", "upstream", remoteURL)
	addRemoteCmd.CombinedOutput() // Ignore error if upstream already exists

	// Fetch upstream
	fetchCmd := exec.Command("git", "-C", repoPath, "fetch", "upstream", defaultBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch upstream failed: %w, output: %s", err, string(output))
	}

	// Reset to upstream
	resetCmd := exec.Command("git", "-C", repoPath, "reset", "--hard", fmt.Sprintf("upstream/%s", defaultBranch))
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset failed: %w, output: %s", err, string(output))
	}

	return nil
}

// CreateAndCheckoutBranch creates a new branch and checks it out
func CreateAndCheckoutBranch(repoPath, branchName string) error {
	checkoutCmd := exec.Command("git", "-C", repoPath, "checkout", "-b", branchName)
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b failed: %w, output: %s", err, string(output))
	}
	return nil
}

// CommitAndPush commits changes and pushes to the remote repository on a specific branch
func CommitAndPush(repoPath, branchName, commitMessage, token string) error {
	// Configure git user for the commit
	configCmds := [][]string{
		{"git", "-C", repoPath, "config", "user.name", "Auto PR Bot"},
		{"git", "-C", repoPath, "config", "user.email", "auto-pr-bot@users.noreply.github.com"},
	}

	for _, cmdArgs := range configCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config failed: %w, output: %s", err, string(output))
		}
	}

	// Add all changes
	addCmd := exec.Command("git", "-C", repoPath, "add", "-A")
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w, output: %s", err, string(output))
	}

	// Check if there are changes to commit
	statusCmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	statusOutput, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git status failed: %w, output: %s", err, string(statusOutput))
	}

	if len(strings.TrimSpace(string(statusOutput))) == 0 {
		return fmt.Errorf("no changes to commit")
	}

	// Commit changes
	commitCmd := exec.Command("git", "-C", repoPath, "commit", "-m", commitMessage)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %w, output: %s", err, string(output))
	}

	// Push changes to the specific branch
	pushCmd := exec.Command("git", "-C", repoPath, "push", "-u", "origin", branchName)
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %w, output: %s", err, string(output))
	}

	return nil
}

// Cleanup removes the cloned repository from /tmp
func Cleanup(clonePath string) error {
	if err := os.RemoveAll(clonePath); err != nil {
		return fmt.Errorf("failed to cleanup: %w", err)
	}
	return nil
}
