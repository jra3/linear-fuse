.PHONY: build install clean test test-cover integration-test integration-test-full run bench-dirs coverage coverage-html

BINARY=linearfs
VERSION?=dev
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-X github.com/jra3/linear-fuse/internal/cmd.Version=$(VERSION) -X github.com/jra3/linear-fuse/internal/cmd.GitCommit=$(COMMIT)"

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/linearfs

install: build
	cp bin/$(BINARY) ~/bin/$(BINARY)

clean:
	rm -rf bin/

test:
	go test ./...

# Run tests with coverage summary
test-cover:
	go test ./... -cover

# Run read-only integration tests (safe for CI, won't hit API limits)
integration-test:
	@if [ -z "$(LINEAR_API_KEY)" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
	LINEARFS_INTEGRATION=1 go test -v -timeout 10m ./internal/integration/...

# Run all integration tests including writes (may hit API limits on free workspaces)
integration-test-full:
	@if [ -z "$(LINEAR_API_KEY)" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
	LINEARFS_INTEGRATION=1 LINEARFS_WRITE_TESTS=1 go test -v -timeout 20m ./internal/integration/...

run: build
	./bin/$(BINARY) mount /tmp/linear

deps:
	go mod tidy

fmt:
	go fmt ./...

lint:
	golangci-lint run

# Generate full coverage report (unit + integration tests)
# Integration tests need -coverpkg to measure coverage of packages they exercise
coverage:
	@echo "Running unit tests with coverage..."
	@go test ./internal/api/... ./internal/cache/... ./internal/config/... ./internal/db/... \
		./internal/marshal/... ./internal/repo/... ./internal/sync/... \
		-coverprofile=coverage-unit.out -covermode=atomic
	@echo "Running integration tests with cross-package coverage..."
	@go test ./internal/integration/... \
		-coverpkg=./internal/fs/...,./internal/repo/...,./internal/db/...,./internal/api/...,./internal/marshal/...,./internal/sync/...,./internal/cache/...,./internal/config/... \
		-coverprofile=coverage-integration.out -covermode=atomic
	@echo "Merging coverage profiles..."
	@echo "mode: atomic" > coverage.out
	@tail -n +2 coverage-unit.out >> coverage.out
	@tail -n +2 coverage-integration.out >> coverage.out
	@rm coverage-unit.out coverage-integration.out
	@go tool cover -func=coverage.out | tail -1
	@echo "Full report: make coverage-html"

# Open coverage report in browser
coverage-html: coverage
	go tool cover -html=coverage.out

bench-dirs: build
	@if [ -z "$(LINEAR_API_KEY)" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
	./scripts/bench-dirs.sh
