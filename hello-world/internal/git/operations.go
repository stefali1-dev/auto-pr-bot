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

// Cleanup removes the cloned repository from /tmp
func Cleanup(clonePath string) error {
	if err := os.RemoveAll(clonePath); err != nil {
		return fmt.Errorf("failed to cleanup: %w", err)
	}
	return nil
}
