BINARY      := lumos
PKG         := github.com/dsetiawan230294/lumos
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
BIN_DIR     := bin

.PHONY: all build test lint tidy clean cross sync-py test-py

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/lumos

# Copy the canonical Python helper + harness into the location the Go binary
# embeds them from. Run this after editing anything under pkg/lumos/python/.
sync-py:
	rm -rf internal/pyharness/python
	mkdir -p internal/pyharness/python/lumos
	cp pkg/lumos/python/harness.py internal/pyharness/python/
	cp pkg/lumos/python/lumos/*.py internal/pyharness/python/lumos/

test:
	go test ./... -race -count=1

# Unit tests for the Python helper (lumos-py). Requires `python3` on PATH.
# Run from the package dir so `lumos` resolves to the canonical source, not
# the vendored copy under internal/pyharness/.
test-py:
	cd pkg/lumos/python && python3 -m unittest discover -s tests -v

cover:
	go test ./... -race -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR) dist coverage.out coverage.html

# Cross-compile release binaries.
cross:
	@mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64  ./cmd/lumos
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64  ./cmd/lumos
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe ./cmd/lumos
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64   ./cmd/lumos
