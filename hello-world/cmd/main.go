package main

import (
	"context"
	"log"

	"hello-world/internal/handler"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// lambdaHandler is the entry point for the Lambda function
func lambdaHandler(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	h, err := handler.New()
	if err != nil {
		log.Printf("Failed to initialize handler: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       `{"error": "Internal server error"}`,
		}, nil
	}

	return h.Handle(ctx, request)
}

func main() {
	lambda.Start(lambdaHandler)
}
