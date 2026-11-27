.PHONY: help build test lint fmt tidy check-secrets check-adapter-canary

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

build: ## go build ./...
	go build ./...

test: ## go test -race ./...
	go test -race ./...

lint: ## go vet ./...; golangci-lint run (if installed)
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; fi

fmt: ## gofmt -w .
	gofmt -w .

tidy: ## go mod tidy
	go mod tidy

check-secrets: ## scan tracked files for realistic secret patterns
	./scripts/check-secret-patterns.sh

check-adapter-canary: ## each integration package with an HTTP client has a credential leak test
	./scripts/test-check-adapter-canary.sh
	./scripts/check-adapter-canary-tests.sh
