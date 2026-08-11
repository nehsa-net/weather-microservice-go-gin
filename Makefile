GO   ?= go
PKG  := ./...

.DEFAULT_GOAL := help

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'

## test: the unit tier — no network, no setup
test:
	$(GO) test -race -timeout=60s $(PKG)

## test-integration: the assembled stack against a stubbed upstream
test-integration:
	$(GO) test -tags=integration -race -timeout=5m ./test/integration/...

## test-e2e: build the binary and drive it as a black box
test-e2e:
	$(GO) test -tags=e2e -timeout=5m ./test/e2e/...

## test-all: every tier, fastest failure first
test-all: test test-integration test-e2e

## cover: unit coverage per function
cover:
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic $(PKG)
	$(GO) tool cover -func=coverage.out | tail -15

## cover-html: line-by-line coverage in a browser
cover-html: cover
	$(GO) tool cover -html=coverage.out

## lint: vet on every tag combination, plus golangci-lint if installed
lint:
	$(GO) vet $(PKG)
	$(GO) vet -tags=integration ./test/...
	$(GO) vet -tags=e2e ./test/...
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run \
		|| echo "golangci-lint not installed; skipping"

## fmt-check: fail if anything is unformatted — the CI form
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

## run: start the service locally (needs OPENWEATHER_API_KEY)
run:
	$(GO) run .

## ci: exactly what the GitHub workflow runs
ci: fmt-check lint test-all

## clean: remove build and coverage artifacts
clean:
	rm -f coverage.out weather-microservice main
	$(GO) clean -testcache

.PHONY: help test test-integration test-e2e test-all cover cover-html lint \
	fmt-check run ci clean
