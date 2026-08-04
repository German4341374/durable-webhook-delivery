.DEFAULT_GOAL := help
.PHONY: help setup fmt lint test test-race build up down scenarios clean

help:
	@echo "setup fmt lint test test-race build up down scenarios clean"

setup:
	./scripts/setup-env.sh

fmt:
	test -z "$$(gofmt -l cmd internal)"

lint:
	go vet ./...

test:
	go test -p 1 -cover ./...

test-race:
	go test -p 1 -race ./...

build:
	go build ./cmd/durable-webhook-delivery
	go build ./cmd/demo-receiver
	docker build --target runtime -t durable-webhook-delivery:local .

up: setup
	docker compose up -d --build

down:
	docker compose down

scenarios: setup
	./scripts/controlled-scenarios.sh

clean:
	docker compose down --volumes --remove-orphans
	rm -rf bin coverage.out
