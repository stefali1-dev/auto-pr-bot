package main

import (
	"context"
	"log"
	"strings"

	"hello-world/internal/handler"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// Entry point for the Lambda function
func lambdaHandler(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Route based on the path
	path := request.Path

	// Handle status endpoint
	if strings.HasPrefix(path, "/status/") {
		h, err := handler.NewStatusHandler()
		if err != nil {
			log.Printf("Failed to initialize status handler: %v", err)
			return events.APIGatewayProxyResponse{
				StatusCode: 500,
				Headers: map[string]string{
					"Content-Type":                 "application/json",
					"Access-Control-Allow-Origin":  "*",
					"Access-Control-Allow-Methods": "GET, OPTIONS",
					"Access-Control-Allow-Headers": "Content-Type",
				},
				Body: `{"error": "Internal server error"}`,
			}, nil
		}
		return h.Handle(ctx, request)
	}

	// Handle process endpoint (default)
	h, err := handler.New()
	if err != nil {
		log.Printf("Failed to initialize handler: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Headers: map[string]string{
				"Content-Type":                 "application/json",
				"Access-Control-Allow-Origin":  "*",
				"Access-Control-Allow-Methods": "POST, OPTIONS",
				"Access-Control-Allow-Headers": "Content-Type",
			},
			Body: `{"error": "Internal server error"}`,
		}, nil
	}

	return h.Handle(ctx, request)
}

func main() {
	lambda.Start(lambdaHandler)
}
