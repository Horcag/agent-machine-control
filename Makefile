.DEFAULT_GOAL := quick

include quality/tool-versions.env

GO_FILES := $(shell git ls-files --cached --others --exclude-standard '*.go')

.PHONY: build test test-race fmt fmt-check mod-check tool-versions vet lint actionlint vuln secrets docs quick check quality hooks graph-build graph-update graph-review clean

build:
	go build ./cmd/...

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	gofmt -w $(GO_FILES)

fmt-check:
	test -z "$$(gofmt -l $(GO_FILES))"

mod-check:
	go mod tidy -diff
	go mod verify

tool-versions:
	sh scripts/check-tool-versions.sh

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

actionlint:
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

secrets:
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --redact --no-banner

docs:
	npx --yes markdownlint-cli2@$(MARKDOWNLINT_VERSION)

quick: fmt-check mod-check tool-versions vet test build

check: quick lint

quality: check test-race actionlint vuln secrets docs

hooks:
	go run github.com/evilmartians/lefthook/v2@$(LEFTHOOK_VERSION) install

graph-build:
	uvx --from code-review-graph==$(CODE_REVIEW_GRAPH_VERSION) code-review-graph build --repo .

graph-update:
	uvx --from code-review-graph==$(CODE_REVIEW_GRAPH_VERSION) code-review-graph update --repo . --brief

graph-review:
	uvx --from code-review-graph==$(CODE_REVIEW_GRAPH_VERSION) code-review-graph detect-changes --repo . --base HEAD --brief

clean:
	go clean ./...
