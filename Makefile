.PHONY: build install clean test run

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

run: build
	./bin/$(BINARY) mount /tmp/linear

deps:
	go mod tidy

fmt:
	go fmt ./...

lint:
	golangci-lint run
