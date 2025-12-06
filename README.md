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

### Request Fields

- **`repositoryUrl`** (required): The full GitHub repository URL to contribute to
  - Example: `"https://github.com/owner/repo"`

- **`modificationPrompt`** (required): A description of what changes you want to make
  - Example: `"Add error handling to the main function"`
  - Example: `"Update README.md to include installation instructions"`

- **`githubUsername`** (optional): A GitHub username to add as a collaborator to the fork
  - If provided, the user will receive an invitation to collaborate on the fork
  - Once accepted, they'll have write access to edit the PR and push changes
  - If omitted, only the bot's account will have access to the fork

### Response

The Lambda returns immediately with a 202 Accepted status:

```json
{
  "status": "processing",
  "message": "Your request is being processed. Check CloudWatch logs for progress.",
  "repository": "https://github.com/owner/repo"
}
```

The actual processing happens asynchronously. Check CloudWatch logs for detailed progress and the final PR URL.

## Requirements

* [AWS CLI](https://aws.amazon.com/cli/) configured with appropriate permissions
* [Docker installed](https://www.docker.com/community-edition)
* [SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/serverless-sam-cli-install.html)
* [Go 1.23+](https://golang.org/doc/install)
* GitHub Personal Access Token (with repo and workflow permissions)
* OpenAI API Key

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

## How It Works

### Async Workflow

The Lambda uses an async invocation pattern to handle long-running operations:

1. **Sync Request** (from API Gateway): Detects the API Gateway call, invokes itself asynchronously, returns 202 Accepted immediately
2. **Async Processing**: The self-invoked Lambda processes the repository, creates the PR, and logs all details to CloudWatch

### Multi-Step LLM Process

The bot uses OpenAI in multiple steps for intelligent code modification:

1. **Analyze**: Examines the repository structure to determine which files to read
2. **Read**: Reads the identified files (with smart truncation for large files)
3. **Plan**: Determines which files need modification based on the prompt
4. **Generate**: Creates complete modified file contents for each file
5. **Apply**: Writes the changes, commits, and pushes to a new timestamped branch

### Multiple PRs Support

Each request creates a unique timestamped branch (`auto-pr-bot/<unix-timestamp>`), allowing multiple concurrent PRs per repository. The bot only closes PRs from the default branch, leaving feature-branch PRs independent.