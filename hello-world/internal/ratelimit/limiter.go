package ratelimit

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	MaxRequestsPerHour = 5
	HourInSeconds      = 3600
)

type Limiter struct {
	client    *dynamodb.Client
	tableName string
}

type RateLimitRecord struct {
	RequestID string `dynamodbav:"requestId"`
	IpAddress string `dynamodbav:"ipAddress"`
	Timestamp int64  `dynamodbav:"timestamp"`
	ExpiresAt int64  `dynamodbav:"expiresAt"`
}

type RateLimitResult struct {
	Allowed       bool
	RequestsUsed  int
	RequestsLimit int
	NextAvailable time.Time
}

func NewLimiter(ctx context.Context) (*Limiter, error) {
	tableName := os.Getenv("STATUS_TABLE_NAME")
	if tableName == "" {
		tableName = "auto-pr-bot-status"
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Limiter{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: tableName,
	}, nil
}

func (l *Limiter) CheckRateLimit(ctx context.Context, ipAddress string) (*RateLimitResult, error) {
	now := time.Now().Unix()
	oneHourAgo := now - HourInSeconds

	// Query DynamoDB for requests from this IP in the last hour
	input := &dynamodb.QueryInput{
		TableName:              aws.String(l.tableName),
		IndexName:              aws.String("IpAddressIndex"),
		KeyConditionExpression: aws.String("ipAddress = :ip AND #ts >= :oneHourAgo"),
		ExpressionAttributeNames: map[string]string{
			"#ts": "timestamp",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":ip":         &types.AttributeValueMemberS{Value: ipAddress},
			":oneHourAgo": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", oneHourAgo)},
		},
	}

	result, err := l.client.Query(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to query rate limit records: %w", err)
	}

	requestCount := len(result.Items)
	allowed := requestCount < MaxRequestsPerHour

	// Calculate when next request will be available
	var nextAvailable time.Time
	if !allowed && len(result.Items) > 0 {
		// Find the oldest request timestamp
		var oldestTimestamp int64 = now
		for _, item := range result.Items {
			var record RateLimitRecord
			if err := attributevalue.UnmarshalMap(item, &record); err == nil {
				if record.Timestamp < oldestTimestamp {
					oldestTimestamp = record.Timestamp
				}
			}
		}
		// Next available is 1 hour after the oldest request
		nextAvailable = time.Unix(oldestTimestamp+HourInSeconds, 0)
	}

	return &RateLimitResult{
		Allowed:       allowed,
		RequestsUsed:  requestCount,
		RequestsLimit: MaxRequestsPerHour,
		NextAvailable: nextAvailable,
	}, nil
}

func (l *Limiter) RecordRequest(ctx context.Context, ipAddress, requestID string) error {
	now := time.Now().Unix()
	expiresAt := now + HourInSeconds + 300 // Expire 5 minutes after the hour window

	// Use a separate key pattern for rate limit records to avoid collision with status records
	// Format: rl#{ipAddress}#{timestamp}
	rateLimitKey := fmt.Sprintf("rl#%s#%d", ipAddress, now)

	record := RateLimitRecord{
		RequestID: rateLimitKey, // Use special key to avoid collision
		IpAddress: ipAddress,
		Timestamp: now,
		ExpiresAt: expiresAt,
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return fmt.Errorf("failed to marshal rate limit record: %w", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String(l.tableName),
		Item:      item,
	}

	_, err = l.client.PutItem(ctx, input)
	if err != nil {
		log.Printf("Warning: Failed to record rate limit in DynamoDB: %v", err)
		return nil // Don't fail the request if rate limit recording fails
	}

	log.Printf("Rate limit recorded for IP %s (key: %s, original requestId: %s)", ipAddress, rateLimitKey, requestID)
	return nil
}
