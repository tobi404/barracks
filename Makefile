BIN       := bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(VERSION)
COVER_MIN ?= 80.0
LINT_VER  := $(shell cat .golangci-lint-version)

.PHONY: build install test cover cover-check lint fmt-check vet golangci fmt clean

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

clean:
	rm -rf $(BIN) coverage.out
