BIN       := bin
MODULE    := github.com/tobi404/barracks
STAMP     := $(MODULE)/internal/buildinfo
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -X $(STAMP).Version=$(VERSION) -X $(STAMP).Commit=$(COMMIT) -X $(STAMP).Date=$(DATE)
COVER_MIN ?= 80.0
LINT_VER  := $(shell cat .golangci-lint-version)
GORL_VER  := $(shell cat .goreleaser-version)

.PHONY: build install test cover cover-check lint fmt-check vet golangci fmt clean \
	release-check release-snapshot

build:
	@mkdir -p $(BIN)
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/barracks .
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/brk ./cmd/brk

install:
	go install -ldflags "$(LDFLAGS)" .
	go install -ldflags "$(LDFLAGS)" ./cmd/brk

test:
	go test -race ./...

cover:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Same gate CI enforces.
cover-check: cover
	@go tool cover -func=coverage.out | awk -v min=$(COVER_MIN) '\
		/^total:/ { \
			seen = 1; \
			gsub(/%/, "", $$3); \
			total = $$3 + 0; \
		} \
		END { \
			if (!seen) { print "no total line in coverage profile"; exit 1; } \
			if (total < min + 0) { \
				printf "total coverage %.1f%% is below the %.1f%% minimum\n", total, min; \
				exit 1; \
			} \
			printf "total coverage %.1f%% (minimum %.1f%%)\n", total, min; \
		}'

# Each check owns its command once; CI invokes the same targets.
lint: fmt-check vet

fmt-check:
	@unformatted=$$(gofmt -l .); \
	status=$$?; \
	if [ $$status -ne 0 ]; then \
		echo "::error::gofmt could not check this tree (exit $$status)"; \
		exit $$status; \
	fi; \
	if [ -n "$$unformatted" ]; then \
		echo "::error::these files are not gofmt-clean (run: make fmt)"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	go vet ./...

# The linter CI runs, at the version CI pins.
golangci:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VER) run ./...

fmt:
	gofmt -w .

# Release packaging, at the version .goreleaser-version pins. The release
# workflow runs the same targets, so a config error is caught here rather than
# on a tag.
release-check:
	go run github.com/goreleaser/goreleaser/v2@$(GORL_VER) check

# Everything a real release does - every platform, archives, checksums, and the
# Homebrew formula - written to dist/ and published nowhere. HOMEBREW_TAP_GITHUB_TOKEN
# is only read when publishing, so a dummy value is enough to render the formula.
release-snapshot:
	HOMEBREW_TAP_GITHUB_TOKEN=$${HOMEBREW_TAP_GITHUB_TOKEN:-snapshot-not-a-real-token} \
		go run github.com/goreleaser/goreleaser/v2@$(GORL_VER) release \
		--snapshot --clean --skip=publish,announce,validate

clean:
	rm -rf $(BIN) coverage.out dist
