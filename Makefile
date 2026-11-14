.PHONY: build test test-integration test-chaos test-prometheus lint run bench

build:
	go build -o bin/ratelimiter ./cmd/server

test:
	go test -race ./...

test-integration:
	go test -tags integration ./...

test-chaos:
	go test -tags 'integration,chaos' -run TestRedisKillAndRecovery -v ./cmd/server

test-prometheus:
	go test -tags 'integration,prometheus' -run TestPrometheusLiveScrape -v ./cmd/server

lint:
	go vet ./...

run:
	go run ./cmd/server

bench:
	go test -run '^$$' -bench . -benchmem ./internal/limiter
