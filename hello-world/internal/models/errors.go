package models

import "errors"

var (
	ErrMissingRepositoryURL      = errors.New("repositoryUrl is required")
	ErrMissingModificationPrompt = errors.New("modificationPrompt is required")
	ErrInvalidRepositoryURL      = errors.New("invalid repository URL format")
	ErrForkFailed                = errors.New("failed to fork repository")
	ErrCloneFailed               = errors.New("failed to clone repository")
	ErrMaxRetriesExceeded        = errors.New("maximum retries exceeded")
)

type RateLimitError struct {
	Error     string        `json:"error"`
	RateLimit RateLimitInfo `json:"rateLimit"`
}

type RateLimitInfo struct {
	Limit      int    `json:"limit"`
	Used       int    `json:"used"`
	ResetAt    int64  `json:"resetAt"`    // Unix timestamp in seconds
	ResetAtISO string `json:"resetAtISO"` // ISO 8601 format
}
