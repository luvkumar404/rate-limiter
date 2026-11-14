.PHONY: build test lint run bench

build:
	go build -o bin/ratelimiter ./cmd/server

test:
	go test -race ./...

lint:
	go vet ./...

run:
	go run ./cmd/server

bench:
	go test -run '^$$' -bench . -benchmem ./internal/limiter
