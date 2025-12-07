package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"hello-world/internal/status"

	"github.com/aws/aws-lambda-go/events"
)

// StatusHandler handles status check requests
type StatusHandler struct {
	tracker *status.Tracker
}

// NewStatusHandler creates a new status handler
func NewStatusHandler() (*StatusHandler, error) {
	tracker, err := status.NewTracker(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to create status tracker: %w", err)
	}

	return &StatusHandler{
		tracker: tracker,
	}, nil
}

// Handle processes the GET /status/{requestId} request
func (h *StatusHandler) Handle(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Extract requestId from path parameters
	requestID, ok := request.PathParameters["requestId"]
	if !ok || requestID == "" {
		return h.errorResponse(400, "Missing requestId in path")
	}

	log.Printf("Status check for request: %s", requestID)

	// Get status from DynamoDB
	statusRecord, err := h.tracker.Get(ctx, requestID)
	if err != nil {
		log.Printf("Failed to get status for %s: %v", requestID, err)
		return h.errorResponse(404, "Request not found")
	}

	// Build response
	response := map[string]interface{}{
		"requestId":  statusRecord.RequestID,
		"status":     statusRecord.Status,
		"message":    statusRecord.Message,
		"step":       statusRecord.Step,
		"timestamp":  statusRecord.Timestamp,
		"repository": statusRecord.Repository,
	}

	// Add optional fields if present
	if statusRecord.PrURL != "" {
		response["prUrl"] = statusRecord.PrURL
	}
	if statusRecord.ErrorDetails != "" {
		response["errorDetails"] = statusRecord.ErrorDetails
	}

	responseBody, err := json.Marshal(response)
	if err != nil {
		return h.errorResponse(500, "Failed to marshal response")
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type":                 "application/json",
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": "GET, OPTIONS",
			"Access-Control-Allow-Headers": "Content-Type",
		},
		Body: string(responseBody),
	}, nil
}

// errorResponse creates an error API Gateway response
func (h *StatusHandler) errorResponse(statusCode int, message string) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type":                 "application/json",
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": "GET, OPTIONS",
			"Access-Control-Allow-Headers": "Content-Type",
		},
		Body: fmt.Sprintf(`{"error": "%s"}`, message),
	}, nil
}
