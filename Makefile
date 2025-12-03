.PHONY: build run test clean lint docker tidy

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X github.com/fusionn-muse/internal/version.Version=$(VERSION)"

# Build binary
build:
	go build $(LDFLAGS) -o fusionn-muse ./cmd/fusionn-muse

# Run locally
run:
	go run ./cmd/fusionn-muse

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-cover:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	rm -f fusionn-muse
	rm -f coverage.out coverage.html

# Run linter
lint:
	golangci-lint run ./...

# Tidy dependencies
tidy:
	go mod tidy

# Docker build
docker:
	docker build --build-arg VERSION=$(VERSION) -t fusionn-muse:$(VERSION) .

# Docker compose commands
docker-run:
	docker compose up -d

docker-logs:
	docker compose logs -f

docker-stop:
	docker compose down

# Install dev tools
tools:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Generate (placeholder for future codegen)
generate:
	go generate ./...

