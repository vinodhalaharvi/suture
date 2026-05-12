.PHONY: all build test test-race cover lint clean demo docker fly-deploy run help

# Forcing the local toolchain because go.dev/dl downloads are blocked
# in some environments; the published go.mod is compatible with Go 1.22+.
GO = GOTOOLCHAIN=local go

all: build test

build: ## Build all binaries
	$(GO) build -o bin/suture-server ./cmd/suture-server
	$(GO) build -o bin/demo ./cmd/demo

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests under the race detector
	$(GO) test -race -count=1 ./...

cover: ## Generate an HTML coverage report
	$(GO) test -coverprofile=cover.out ./...
	$(GO) tool cover -html=cover.out -o coverage.html
	@echo "open coverage.html"

lint: ## go vet + gofmt check
	$(GO) vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt found issues:"; \
		gofmt -l .; \
		exit 1; \
	fi

clean: ## Remove build artifacts
	rm -rf bin cover.out coverage.html

demo: build ## Build then run the demo against the public HAPI FHIR sandbox
	./bin/demo -tool get_patient_summary -patient 1234567

run: build ## Run suture-server on :8080
	./bin/suture-server

docker: ## Build the Docker image
	docker build -t suture-server:latest .

fly-deploy: ## Deploy to Fly.io (requires `fly` CLI and `fly auth login`)
	fly deploy

help: ## Print this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "  %-15s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
