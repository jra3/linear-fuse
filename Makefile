.PHONY: build install clean test test-cover integration-test integration-test-full run bench-dirs coverage coverage-html \
        install-service uninstall-service enable-service disable-service start stop restart status

BINARY=linearfs
VERSION?=dev
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-X github.com/jra3/linear-fuse/internal/cmd.Version=$(VERSION) -X github.com/jra3/linear-fuse/internal/cmd.GitCommit=$(COMMIT)"

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/linearfs

install: build
	mkdir -p ~/.local/bin
	cp bin/$(BINARY) ~/.local/bin/$(BINARY)

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
# Uses -coverpkg to measure cross-package coverage from integration tests
coverage:
	@go test ./... -coverprofile=coverage.out -coverpkg=./internal/...
	@go tool cover -func=coverage.out | tail -1
	@echo "Full report: make coverage-html"

# Open coverage report in browser
coverage-html: coverage
	go tool cover -html=coverage.out

bench-dirs: build
	@if [ -z "$(LINEAR_API_KEY)" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
	./scripts/bench-dirs.sh

# Default mount point (~ expands in shell context)
MOUNT_POINT ?= $(HOME)/linear

# Systemd service installation (Linux only)
install-service: install
	@echo "Installing systemd user service..."
	@mkdir -p ~/.config/systemd/user
	@cp contrib/systemd/linearfs.service ~/.config/systemd/user/
	@mkdir -p ~/.config/linearfs
	@if [ ! -f ~/.config/linearfs/env ]; then \
		echo "LINEARFS_MOUNT=$(MOUNT_POINT)" > ~/.config/linearfs/env; \
		echo "Created ~/.config/linearfs/env with LINEARFS_MOUNT=$(MOUNT_POINT)"; \
	else \
		echo "~/.config/linearfs/env already exists, not overwriting"; \
	fi
	@systemctl --user daemon-reload
	@echo "Service installed. Run 'make enable-service' to enable on login, 'make start' to start now."

uninstall-service:
	@echo "Removing systemd user service..."
	-@systemctl --user stop linearfs.service 2>/dev/null || true
	-@systemctl --user disable linearfs.service 2>/dev/null || true
	@rm -f ~/.config/systemd/user/linearfs.service
	@systemctl --user daemon-reload
	@echo "Service removed. Config files in ~/.config/linearfs/ left intact."

enable-service:
	systemctl --user enable linearfs.service
	@echo "Service will start on login."

disable-service:
	systemctl --user disable linearfs.service

start:
	systemctl --user start linearfs.service

stop:
	systemctl --user stop linearfs.service

restart:
	systemctl --user restart linearfs.service

status:
	systemctl --user status linearfs.service
