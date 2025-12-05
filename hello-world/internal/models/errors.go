package models

import "errors"

var (
	ErrMissingRepositoryURL      = errors.New("repositoryUrl is required")
	ErrMissingGitHubUsername     = errors.New("githubUsername is required")
	ErrMissingModificationPrompt = errors.New("modificationPrompt is required")
	ErrInvalidRepositoryURL      = errors.New("invalid repository URL format")
	ErrForkFailed                = errors.New("failed to fork repository")
	ErrCloneFailed               = errors.New("failed to clone repository")
	ErrMaxRetriesExceeded        = errors.New("maximum retries exceeded")
)
