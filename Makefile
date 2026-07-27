BIN     := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install test cover lint fmt clean

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

lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "run: gofmt -w ." && exit 1)
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN) coverage.out
