.PHONY: build test test-integration lint run bench

build:
	go build -o bin/ratelimiter ./cmd/server

test:
	go test -race ./...

test-integration:
	go test -tags integration ./...

lint:
	go vet ./...

run:
	go run ./cmd/server

bench:
	go test -run '^$$' -bench . -benchmem ./internal/limiter
