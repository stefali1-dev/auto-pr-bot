package models

// Request represents the incoming Lambda event payload
type Request struct {
	RepositoryURL      string `json:"repositoryUrl"`
	GitHubUsername     string `json:"githubUsername"`
	ModificationPrompt string `json:"modificationPrompt"`
}

// Validate checks if the request has all required fields
func (r *Request) Validate() error {
	if r.RepositoryURL == "" {
		return ErrMissingRepositoryURL
	}
	if r.ModificationPrompt == "" {
		return ErrMissingModificationPrompt
	}
	return nil
}
