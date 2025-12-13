# auto-pr-bot

An AWS Lambda function that automates contributions to open source repositories through an intelligent, multi-step LLM-powered workflow.

## Overview

This bot accepts HTTP requests to automatically:
1. Fork a target repository (or reuse existing fork)
2. Clone the fork and reset it to match upstream
3. Create a new timestamped feature branch
4. Use OpenAI to analyze the repository and determine which files to modify
5. Generate and apply code modifications based on your prompt
6. Commit and push changes
7. Create a Pull Request to the upstream repository
8. Optionally add a GitHub user as a collaborator to the fork (giving them write access to edit the PR)

## API Request Format

Send a POST request to the Lambda endpoint with the following JSON body:

```json
{
  "repositoryUrl": "https://github.com/owner/repo",
  "modificationPrompt": "Description of the changes you want to make",
  "githubUsername": "optional-github-username"
}
```

Example Curl:

```bash
curl -X POST https://c3wy3ydime.execute-api.eu-central-1.amazonaws.com/Prod/process \
  -H "Content-Type: application/json" \
  -d '{
    "repositoryUrl": "https://github.com/FrentescuCezar/FII-BachelorThesis",
    "modificationPrompt": "Add a comment to the README.md file saying Hello World",
    "githubUsername": "stefali1-dev"
  }'
```

## Environment Variables

Create an `env.json` file (see `env.json.example`) with:

```json
{
  "AutoPRBotFunction": {
    "GITHUB_TOKEN": "your-github-token",
    "OPENAI_API_KEY": "your-openai-api-key"
  }
}
```

## Local Development

### Building

Build the Docker image and Lambda function:

```bash
sam build
```

### Local Testing

Invoke the function locally with a test event:

```bash
sam local invoke -e events/test-event.json --env-vars env.json
```

**Note:** Local invocation processes synchronously. In AWS, the Lambda detects API Gateway calls and invokes itself asynchronously to avoid the 29-second timeout.

## Deployment

Deploy to AWS for the first time:

```bash
sam deploy --guided
```

For subsequent deployments:

```bash
sam deploy
```

The deployment will output the API Gateway endpoint URL that you can use to invoke the Lambda function.
