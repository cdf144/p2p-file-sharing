.PHONY: test build clean lint fmt vet deps help

GO_VERSION := 1.24
COVERAGE_FILE := coverage.out
BUILD_DIR := dist
GOLANGCI_LINT_VERSION := v2.1.6

help:
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@echo '  help            Show this help message'
	@echo '  deps            Download and verify Go modules'
	@echo '  fmt             Format Go code and run goimports'
	@echo '  vet             Run go vet'
	@echo '  lint            Run golangci-lint'
	@echo '  test            Run tests with coverage'
	@echo '  coverage        Generate coverage report'
	@echo '  bench           Run benchmarks'
	@echo '  build           Build all binaries'
	@echo '  clean           Clean build artifacts'
	@echo '  ci              Run CI pipeline (deps, fmt, vet, lint, test-all)'
	@echo '  install-tools   Install required development tools'

deps:
	go mod download
	go mod verify

fmt:
	gofumpt -w .
	goimports -w .

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	chmod +x scripts/validate_chunks.sh
	go test -v -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...

coverage: test
	go tool cover -html=$(COVERAGE_FILE) -o coverage.html
	go tool cover -func=$(COVERAGE_FILE)

bench:
	go test -v -bench=. -benchmem ./pkg/corepeer

build:
	./scripts/build_all.sh

clean:
	rm -rf $(BUILD_DIR)
	rm -f $(COVERAGE_FILE) coverage.html
	rm -rf gui/peer/build/bin

ci: deps fmt vet lint test

install-tools:
	go install golang.org/x/tools/cmd/goimports@latest
	go install mvdan.cc/gofumpt@latest
	go install github.com/wailsapp/wails/v2/cmd/wails@latest

	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint version; \
	else \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	fi

	@echo "✅ All tools installed successfully!"
	@echo "Make sure $(shell go env GOPATH)/bin is in your PATH"
