.PHONY: build install clean test integration-test integration-test-full run bench-dirs

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

bench-dirs: build
	@if [ -z "$(LINEAR_API_KEY)" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
	./scripts/bench-dirs.sh
