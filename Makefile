.DEFAULT_GOAL := help

BIN := bin/steward

.PHONY: help install build run dev test lint format

help:
	@printf "Usage: make <target>\n\n"
	@printf "Targets:\n"
	@printf "  install  Install Go modules and Web dependencies\n"
	@printf "  build    Build Web assets and the steward binary\n"
	@printf "  run      Build and start the local server\n"
	@printf "  dev      Start API and Vite dev server with Web hot reload\n"
	@printf "  test     Run backend tests and Web build\n"
	@printf "  lint     Run Go vet and Web type checks\n"
	@printf "  format   Format Go and Web sources\n"

install:
	go mod download
	npm --prefix web ci

build:
	npm --prefix web run build
	rm -f web/tsconfig.tsbuildinfo
	mkdir -p bin
	go build -o $(BIN) ./cmd/steward
	@printf "Built %s/%s\n" "$(CURDIR)" "$(BIN)"

run: build
	./$(BIN) server start

dev:
	@set -e; \
		go run ./cmd/steward server start & \
		api_pid=$$!; \
		trap 'kill $$api_pid >/dev/null 2>&1 || true; wait $$api_pid >/dev/null 2>&1 || true' INT TERM EXIT; \
		npm --prefix web run dev

test:
	go test ./...
	npm --prefix web run build
	rm -f web/tsconfig.tsbuildinfo

lint:
	go vet ./...
	npm --prefix web run lint

format:
	go fmt ./...
	npm --prefix web run format
	rm -f web/tsconfig.tsbuildinfo
