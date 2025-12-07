package status

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Represents the current status of a PR request
type Status string

const (
	StatusPending    Status = "pending"
	StatusForking    Status = "forking"
	StatusCloning    Status = "cloning"
	StatusAnalyzing  Status = "analyzing"
	StatusModifying  Status = "modifying"
	StatusCommitting Status = "committing"
	StatusCreatingPR Status = "creating_pr"
	StatusCompleted  Status = "completed"
	StatusError      Status = "error"
)

// Represents a status record in DynamoDB
type StatusRecord struct {
	RequestID    string `dynamodbav:"requestId"`
	Status       string `dynamodbav:"status"`
	Message      string `dynamodbav:"message"`
	Step         int    `dynamodbav:"step"`
	Timestamp    int64  `dynamodbav:"timestamp"`
	PrURL        string `dynamodbav:"prUrl,omitempty"`
	ErrorDetails string `dynamodbav:"errorDetails,omitempty"`
	Repository   string `dynamodbav:"repository"`
	ExpiresAt    int64  `dynamodbav:"expiresAt"`
}

// Handles DynamoDB operations for status tracking
type Tracker struct {
	client    *dynamodb.Client
	tableName string
}

// Creates a new status tracker
func NewTracker(ctx context.Context) (*Tracker, error) {
	tableName := os.Getenv("STATUS_TABLE_NAME")
	if tableName == "" {
		tableName = "auto-pr-bot-status"
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Tracker{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: tableName,
	}, nil
}

// Updates the status for a request
func (t *Tracker) Update(ctx context.Context, requestID string, status Status, message string, step int, repository string) error {
	record := StatusRecord{
		RequestID:  requestID,
		Status:     string(status),
		Message:    message,
		Step:       step,
		Timestamp:  time.Now().Unix(),
		Repository: repository,
		ExpiresAt:  time.Now().Add(48 * time.Hour).Unix(), // Auto-delete after 48 hours
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String(t.tableName),
		Item:      item,
	}

	_, err = t.client.PutItem(ctx, input)
	if err != nil {
		log.Printf("Warning: Failed to update status in DynamoDB: %v", err)
		// Don't fail the entire process if status update fails
		return nil
	}

	log.Printf("Status updated: %s - %s (step %d)", requestID, status, step)
	return nil
}

// Marks a request as completed with PR URL
func (t *Tracker) Complete(ctx context.Context, requestID string, prURL string, repository string) error {
	record := StatusRecord{
		RequestID:  requestID,
		Status:     string(StatusCompleted),
		Message:    "Pull request created successfully",
		Step:       9,
		Timestamp:  time.Now().Unix(),
		PrURL:      prURL,
		Repository: repository,
		ExpiresAt:  time.Now().Add(48 * time.Hour).Unix(),
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String(t.tableName),
		Item:      item,
	}

	_, err = t.client.PutItem(ctx, input)
	if err != nil {
		log.Printf("Warning: Failed to update status in DynamoDB: %v", err)
		return nil
	}

	log.Printf("Status completed: %s - PR: %s", requestID, prURL)
	return nil
}

// Marks a request as failed
func (t *Tracker) Error(ctx context.Context, requestID string, errorMsg string, repository string) error {
	record := StatusRecord{
		RequestID:    requestID,
		Status:       string(StatusError),
		Message:      "An error occurred during processing",
		ErrorDetails: errorMsg,
		Timestamp:    time.Now().Unix(),
		Repository:   repository,
		ExpiresAt:    time.Now().Add(48 * time.Hour).Unix(),
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String(t.tableName),
		Item:      item,
	}

	_, err = t.client.PutItem(ctx, input)
	if err != nil {
		log.Printf("Warning: Failed to update error status in DynamoDB: %v", err)
		return nil
	}

	log.Printf("Status error: %s - %s", requestID, errorMsg)
	return nil
}

// Retrieves the status for a request
func (t *Tracker) Get(ctx context.Context, requestID string) (*StatusRecord, error) {
	input := &dynamodb.GetItemInput{
		TableName: aws.String(t.tableName),
		Key: map[string]types.AttributeValue{
			"requestId": &types.AttributeValueMemberS{Value: requestID},
		},
	}

	result, err := t.client.GetItem(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get item: %w", err)
	}

	if result.Item == nil {
		return nil, fmt.Errorf("request not found")
	}

	var record StatusRecord
	err = attributevalue.UnmarshalMap(result.Item, &record)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal record: %w", err)
	}

	return &record, nil
}

// Returns the step number for a status
func ParseStepFromStatus(status Status) int {
	steps := map[Status]int{
		StatusPending:    0,
		StatusForking:    1,
		StatusCloning:    2,
		StatusAnalyzing:  3,
		StatusModifying:  4,
		StatusCommitting: 5,
		StatusCreatingPR: 6,
		StatusCompleted:  9,
		StatusError:      -1,
	}

	if step, ok := steps[status]; ok {
		return step
	}
	return 0
}

// Formats a Unix timestamp to a human-readable string
func FormatTimestamp(timestamp int64) string {
	return time.Unix(timestamp, 0).Format(time.RFC3339)
}

// Parses a string timestamp to Unix time
func ParseTimestamp(timestampStr string) (int64, error) {
	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp: %w", err)
	}
	return timestamp, nil
}
