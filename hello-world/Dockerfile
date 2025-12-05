# Build stage
FROM public.ecr.aws/docker/library/golang:1.23 as build-image

WORKDIR /src

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /lambda-handler ./cmd/main.go

# Runtime stage
FROM public.ecr.aws/lambda/provided:al2023

# Install git and CA certificates (AL2023 uses dnf)
RUN dnf update -y && \
    dnf install -y git ca-certificates && \
    dnf clean all && \
    rm -rf /var/cache/dnf

# Copy the built binary from build stage
COPY --from=build-image /lambda-handler /lambda-handler

# Set the entrypoint
ENTRYPOINT ["/lambda-handler"]
