.PHONY: build install clean test test-cover integration-tests integration-tests-ro integration-tests-rw run bench-dirs coverage coverage-html \
        install-service uninstall-service enable-service disable-service start stop restart status

BINARY=linearfs
VERSION?=dev
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
# Commit date (not wall-clock) keeps `make build` reproducible for a given commit.
DATE=$(shell git show -s --format=%cI HEAD 2>/dev/null || echo "unknown")
PKG=github.com/jra3/linear-fuse/internal/cmd
LDFLAGS=-ldflags "-X $(PKG).Version=$(VERSION) -X $(PKG).GitCommit=$(COMMIT) -X $(PKG).BuildDate=$(DATE)"

build:
	go build -trimpath $(LDFLAGS) -o bin/$(BINARY) ./cmd/linearfs

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

# Live runs take their credentials from .env (gitignored), never from the ambient
# environment. LINEAR_API_KEY in a developer's shell is normally the key for the
# workspace they actually work in, and a live run of this suite reads that whole
# workspace and — in write mode — creates issues and projects in it. .env is the
# one file whose contents are chosen for testing, so the live targets read it and
# refuse to run without it. Pair it with LINEARFS_TEST_TEAM (see .env.example):
# naming the team makes a key for the wrong workspace fail during setup.
#
# It deliberately OVERRIDES anything already exported: an ambient key silently
# winning over the file is the exact failure this exists to prevent.
LIVE_ENV = test -f .env || { echo "no .env: copy .env.example and fill in the TEST workspace's key"; exit 1; }; \
	set -a; . ./.env; set +a; \
	test -n "$$LINEAR_API_KEY" || { echo "LINEAR_API_KEY missing from .env (see .env.example)"; exit 1; };

# Run the integration suite against the LIVE Linear API, READS ONLY. Consumes real
# API quota — the offline fixture suite is plain `make test`, which needs no key.
# LINEARFS_LIVE_API is what liveAPIMode reads; both targets demanded a key and then
# ran offline without it (#386). The fixture-mode write-contract guards that used to
# ride along here (they wrote through the mount) skip under a live key, so "reads
# only" is an enforced property, not a claim.
# The -timeout is a budget the live suite splits with its setup: the store-
# readiness gate (waitForInitialSync) spends up to a third of it waiting for the
# cold-start full sync, so this must leave the tests themselves room.
integration-tests-ro:
	@$(LIVE_ENV) LINEARFS_LIVE_API=1 go test -v -timeout 15m ./internal/integration/...

# Run the integration suite including the write tests. This CREATES AND MODIFIES
# REAL LINEAR DATA and may hit API limits on free workspaces.
integration-tests-rw:
	@$(LIVE_ENV) LINEARFS_LIVE_API=1 LINEARFS_WRITE_TESTS=1 go test -v -timeout 25m ./internal/integration/...

# Both, in that order. -rw is a SUPERSET of -ro, not a disjoint half: WRITE_TESTS=1
# only ADDS the 55 write tests, so the read suite runs twice. What this buys is
# sequencing — a full zero-mutation pass has to go green before anything touches the
# workspace. Sub-makes (not prerequisites) so -j can't reorder them.
integration-tests:
	$(MAKE) integration-tests-ro
	$(MAKE) integration-tests-rw

run: build
	./bin/$(BINARY) mount /tmp/linear

deps:
	go mod tidy

fmt:
	go fmt ./...

lint:
	golangci-lint run

# Pinned; the CI lint gate runs exactly this (no local install needed)
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...

# The "no bypass" grep-rule guarding the fs name/target safety chokepoint
# (safeName, #345): fails if a builder returns a raw remote name field without
# routing through safeName(). CI runs this alongside staticcheck.
check-safename:
	./scripts/check-safename.sh

# Pinned so regeneration doesn't churn version comments in generated files
sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

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
	@if [ -z "$$LINEAR_API_KEY" ]; then echo "LINEAR_API_KEY required"; exit 1; fi
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
