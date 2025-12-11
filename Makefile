.PHONY: build clean install test fmt vet lint

# Binary name
BINARY_NAME=linear-fuse

# Build the binary
build:
	go build -o $(BINARY_NAME) ./cmd/linear-fuse

# Install the binary to $GOPATH/bin
install:
	go install ./cmd/linear-fuse

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	go clean

# Run tests
test:
	go test -v ./...

# Format code
fmt:
	go fmt ./...

# Run go vet
vet:
	go vet ./...

# Run static analysis (requires golangci-lint)
lint:
	golangci-lint run

# Run all checks
check: fmt vet test

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_NAME)-linux-amd64 ./cmd/linear-fuse
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY_NAME)-darwin-amd64 ./cmd/linear-fuse
	GOOS=darwin GOARCH=arm64 go build -o $(BINARY_NAME)-darwin-arm64 ./cmd/linear-fuse

# Help
help:
	@echo "Available targets:"
	@echo "  build      - Build the binary"
	@echo "  install    - Install to GOPATH/bin"
	@echo "  clean      - Remove build artifacts"
	@echo "  test       - Run tests"
	@echo "  fmt        - Format code"
	@echo "  vet        - Run go vet"
	@echo "  lint       - Run linter (requires golangci-lint)"
	@echo "  check      - Run fmt, vet, and test"
	@echo "  build-all  - Build for multiple platforms"
