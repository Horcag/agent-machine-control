.DEFAULT_GOAL := check

.PHONY: build test test-race fmt fmt-check vet lint check clean

build:
	go build ./cmd/...

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	test -z "$$(gofmt -l .)"

vet:
	go vet ./...

lint:
	golangci-lint run

check: fmt-check vet test build

clean:
	go clean ./...
