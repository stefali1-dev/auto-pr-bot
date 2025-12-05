package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	openAIAPIURL = "https://api.openai.com/v1/chat/completions"
	gpt5Mini     = "gpt-5-mini"
)

// Client wraps OpenAI API interactions
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new OpenAI client
func NewClient() (*Client, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is required")
	}

	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

// FilesToReadResponse represents the structured output from the first LLM call
type FilesToReadResponse struct {
	FilesToRead []string `json:"filesToRead"`
}

// FilesToModifyResponse represents the structured output from the second LLM call
type FilesToModifyResponse struct {
	FilesToModify []string `json:"filesToModify"`
	Explanation   string   `json:"explanation"`
}

// FileModificationResponse represents the modified file content from the third LLM call
type FileModificationResponse struct {
	FilePath        string `json:"filePath"`
	ModifiedContent string `json:"modifiedContent"`
}

// ConversationHistory maintains the context of LLM conversations
type ConversationHistory struct {
	Messages []Message
}

// AddMessage adds a message to the conversation history
func (ch *ConversationHistory) AddMessage(role, content string) {
	ch.Messages = append(ch.Messages, Message{Role: role, Content: content})
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest represents the request to OpenAI
type ChatCompletionRequest struct {
	Model               string    `json:"model"`
	Messages            []Message `json:"messages"`
	MaxCompletionTokens int       `json:"max_completion_tokens"`
	ResponseFormat      *struct {
		Type       string                 `json:"type"`
		JSONSchema map[string]interface{} `json:"json_schema,omitempty"`
	} `json:"response_format,omitempty"`
}

// ChatCompletionResponse represents the response from OpenAI
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// AnalyzeRepositoryForFiles asks the LLM which files it needs to read to understand the modification request
func (c *Client) AnalyzeRepositoryForFiles(ctx context.Context, fileStructure, modificationPrompt string) (*ConversationHistory, []string, error) {
	history := &ConversationHistory{}

	systemPrompt := `You are an expert software engineer analyzing a repository to determine which files you need to read to complete a modification request.

Your task:
1. Analyze the repository file structure
2. Determine which files you need to read to understand the codebase and complete the requested modification
3. Include files that:
   - Are directly mentioned in the modification request
   - Might be affected by the changes
   - Are needed to understand the context (e.g., main files, configuration files)
   - Contain related functionality

Only include text-based source code files that you can read. Avoid binary files, images, or other non-text files.

Return ONLY a JSON object with this structure:
{
  "filesToRead": ["path/to/file1.ext", "path/to/file2.ext"]
}

Be thorough but selective - only include files that are actually necessary.`

	userPrompt := fmt.Sprintf(`Repository file structure:
%s

Modification request:
%s

Which files do I need to read?`, fileStructure, modificationPrompt)

	history.AddMessage("system", systemPrompt)
	history.AddMessage("user", userPrompt)

	// Create the request with structured JSON output
	reqBody := ChatCompletionRequest{
		Model:               gpt5Mini,
		Messages:            history.Messages,
		MaxCompletionTokens: 1000,
		ResponseFormat: &struct {
			Type       string                 `json:"type"`
			JSONSchema map[string]interface{} `json:"json_schema,omitempty"`
		}{
			Type: "json_object",
		},
	}

	response, err := c.makeAPICall(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	history.AddMessage("assistant", response)

	// Parse the JSON response
	var filesResponse FilesToReadResponse
	if err := json.Unmarshal([]byte(response), &filesResponse); err != nil {
		return nil, nil, fmt.Errorf("failed to parse files to read: %w", err)
	}

	return history, filesResponse.FilesToRead, nil
}

// DetermineFilesToModify asks the LLM which files need to be modified after reading the relevant files
func (c *Client) DetermineFilesToModify(ctx context.Context, history *ConversationHistory, fileContents map[string]string, modificationPrompt string) ([]string, string, error) {
	// Build file contents section
	var contentBuilder strings.Builder
	contentBuilder.WriteString("Here are the contents of the files I read:\n\n")
	for filePath, content := range fileContents {
		contentBuilder.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", filePath, content))
	}

	userPrompt := fmt.Sprintf(`%s
Now that you have read the necessary files, determine which files need to be modified to complete this request:
%s

Return ONLY a JSON object with this structure:
{
  "filesToModify": ["path/to/file1.ext", "path/to/file2.ext"],
  "explanation": "Brief explanation of what changes are needed"
}`, contentBuilder.String(), modificationPrompt)

	history.AddMessage("user", userPrompt)

	reqBody := ChatCompletionRequest{
		Model:               gpt5Mini,
		Messages:            history.Messages,
		MaxCompletionTokens: 1500,
		ResponseFormat: &struct {
			Type       string                 `json:"type"`
			JSONSchema map[string]interface{} `json:"json_schema,omitempty"`
		}{
			Type: "json_object",
		},
	}

	response, err := c.makeAPICall(ctx, reqBody)
	if err != nil {
		return nil, "", err
	}

	history.AddMessage("assistant", response)

	// Parse the JSON response
	var modifyResponse FilesToModifyResponse
	if err := json.Unmarshal([]byte(response), &modifyResponse); err != nil {
		return nil, "", fmt.Errorf("failed to parse files to modify: %w", err)
	}

	return modifyResponse.FilesToModify, modifyResponse.Explanation, nil
}

// GenerateModifiedFile asks the LLM to generate the complete modified content for a specific file
func (c *Client) GenerateModifiedFile(ctx context.Context, history *ConversationHistory, filePath, originalContent, modificationPrompt string) (string, error) {
	userPrompt := fmt.Sprintf(`Please provide the complete modified content for the file: %s

Original content:
%s

Modification request:
%s

Return the COMPLETE file content with all the necessary changes applied. Include ALL lines of the file, not just the changed parts.
Do not use placeholders like "... rest of the file ..." - provide the full file.

Return it as plain text, not JSON. Just the file content exactly as it should be written to disk.`, filePath, originalContent, modificationPrompt)

	// Create a temporary conversation for this file to keep it focused
	tempHistory := &ConversationHistory{
		Messages: make([]Message, len(history.Messages)),
	}
	copy(tempHistory.Messages, history.Messages)
	tempHistory.AddMessage("user", userPrompt)

	reqBody := ChatCompletionRequest{
		Model:               gpt5Mini,
		Messages:            tempHistory.Messages,
		MaxCompletionTokens: 4000,
	}

	response, err := c.makeAPICall(ctx, reqBody)
	if err != nil {
		return "", err
	}

	return response, nil
}

// makeAPICall handles the HTTP request to OpenAI API with retry logic
func (c *Client) makeAPICall(ctx context.Context, reqBody ChatCompletionRequest) (string, error) {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			log.Printf("Retrying OpenAI API call after %v (attempt %d/%d)", backoff, attempt+1, maxRetries)
			time.Sleep(backoff)
		}

		response, err := c.doAPICall(ctx, reqBody)
		if err == nil {
			return response, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			return "", err
		}

		log.Printf("Retryable error encountered: %v", err)
	}

	return "", fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

// doAPICall performs a single API call without retry logic
func (c *Client) doAPICall(ctx context.Context, reqBody ChatCompletionRequest) (string, error) {
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", openAIAPIURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &APIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	var completion ChatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}

	return completion.Choices[0].Message.Content, nil
}

// APIError represents an error from the OpenAI API
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("OpenAI API error (status %d): %s", e.StatusCode, e.Message)
}

// isRetryableError determines if an error should be retried
func isRetryableError(err error) bool {
	var apiErr *APIError
	if errors, ok := err.(*APIError); ok {
		apiErr = errors
	}

	if apiErr != nil {
		// Retry on server errors and rate limiting
		return apiErr.StatusCode == 429 || // Too Many Requests
			apiErr.StatusCode >= 500 // Server errors (500, 502, 503, 504)
	}

	// Retry on network/timeout errors
	// These would be wrapped in the error message from http.Client.Do
	return strings.Contains(err.Error(), "timeout") ||
		strings.Contains(err.Error(), "connection") ||
		strings.Contains(err.Error(), "network")
}
