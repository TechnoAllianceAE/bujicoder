BINARY    = buji
MODULE    = github.com/TechnoAllianceAE/bujicoder
VERSION  ?= dev
COMMIT    = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME = $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
  -X $(MODULE)/shared/buildinfo.Version=$(VERSION) \
  -X $(MODULE)/shared/buildinfo.Commit=$(COMMIT) \
  -X $(MODULE)/shared/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build install test lint fmt clean dist

## Build the CLI binary
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cli/cmd/buji

## Install to ~/.local/bin/
install: build
	mkdir -p $(HOME)/.local/bin
	cp bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "Installed $(BINARY) to $(HOME)/.local/bin/$(BINARY)"

## Run all tests
test:
	go test ./... -race

## Run tests with coverage report
test-coverage:
	go test ./... -race -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Lint with golangci-lint
lint:
	golangci-lint run ./...

## Format code
fmt:
	gofmt -w .
	goimports -w .

## Clean build artifacts
clean:
	rm -rf bin/ coverage.out coverage.html

## Cross-compile for all platforms
dist:
	@mkdir -p dist
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_amd64  ./cli/cmd/buji
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_arm64  ./cli/cmd/buji
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_amd64   ./cli/cmd/buji
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_arm64   ./cli/cmd/buji
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_windows_amd64.exe ./cli/cmd/buji
	@echo "Binaries in dist/"

## Create a GitHub release (requires gh CLI)
release: dist
ifndef VERSION
	$(error VERSION is required. Usage: make release VERSION=x.y.z)
endif
	gh release create v$(VERSION) dist/* --title "v$(VERSION)" --generate-notes
