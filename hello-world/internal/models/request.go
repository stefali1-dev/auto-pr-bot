package models

type Request struct {
	RepositoryURL      string `json:"repositoryUrl"`
	GitHubUsername     string `json:"githubUsername"`
	ModificationPrompt string `json:"modificationPrompt"`
}

type RequestWithID struct {
	Request   `json:",inline"`
	RequestID string `json:"requestId"`
}

func (r *Request) Validate() error {
	if r.RepositoryURL == "" {
		return ErrMissingRepositoryURL
	}
	if r.ModificationPrompt == "" {
		return ErrMissingModificationPrompt
	}
	return nil
}
