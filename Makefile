.DEFAULT_GOAL := help

BIN := bin/steward
DEV_PORTS := 5858 8585

.PHONY: help install build run stop dev test lint format

help:
	@printf "Usage: make <target>\n\n"
	@printf "Targets:\n"
	@printf "  install  Install Go modules and Web dependencies\n"
	@printf "  build    Build Web assets and the steward binary\n"
	@printf "  run      Build and start the local server\n"
	@printf "  stop     Stop API and Web dev servers by port\n"
	@printf "  dev      Start API and Vite dev server with Web hot reload\n"
	@printf "  test     Run backend and Web tests\n"
	@printf "  lint     Run Go vet and Web type checks\n"
	@printf "  format   Format Go and Web sources\n"

install:
	go mod download
	npm --prefix web ci

build:
	npm --prefix web run build
	rm -f web/tsconfig.tsbuildinfo
	mkdir -p bin
	go build -tags withassets -o $(BIN) ./cmd/steward
	@printf "Built %s/%s\n" "$(CURDIR)" "$(BIN)"

run: build
	./$(BIN) server start

stop:
	@stopped=0; \
	for port in $(DEV_PORTS); do \
		pids=$$(lsof -nP -tiTCP:$$port -sTCP:LISTEN 2>/dev/null || true); \
		if [ -n "$$pids" ]; then \
			printf 'Stopping processes on port %s: %s\n' "$$port" "$$(printf '%s' "$$pids" | tr '\n' ' ')"; \
			kill $$pids; \
			stopped=1; \
		fi; \
	done; \
	if [ $$stopped -eq 0 ]; then \
		printf 'No development servers are listening on ports %s\n' "$(DEV_PORTS)"; \
	fi

dev:
	@set -e; \
		dev_token=$$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n'); \
		dev_key="$${STEWARD_CREDENTIAL_MASTER_KEY:-}"; \
		if [ -z "$$dev_key" ]; then \
			mkdir -p .steward; \
			dev_key_file=.steward/dev-credential-master-key; \
			if [ -f "$$dev_key_file" ]; then \
				dev_key=$$(tr -d '\r\n' < "$$dev_key_file"); \
			else \
				dev_key=$$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n' | xxd -r -p | base64); \
				umask 077; \
				printf '%s\n' "$$dev_key" > "$$dev_key_file"; \
			fi; \
		fi; \
		test $${#dev_token} -eq 64; \
		dev_dir=$$(mktemp -d "$${TMPDIR:-/tmp}/steward-dev.XXXXXX"); \
		api_pid=; \
		trap 'if [ -n "$$api_pid" ]; then kill $$api_pid >/dev/null 2>&1 || true; wait $$api_pid >/dev/null 2>&1 || true; fi; rm -rf "$$dev_dir"' INT TERM EXIT; \
		go build -o "$$dev_dir/steward" ./cmd/steward; \
		STEWARD_AUTH_TOKEN=$$dev_token STEWARD_CREDENTIAL_MASTER_KEY=$$dev_key "$$dev_dir/steward" server start & \
		api_pid=$$!; \
		ready=0; \
		attempt=0; \
		while [ $$attempt -lt 300 ]; do \
			if curl --silent --fail --max-time 1 -H "Authorization: Bearer $$dev_token" http://127.0.0.1:8585/api/providers/catalog >/dev/null 2>&1; then \
				ready=1; \
				break; \
			fi; \
			if ! kill -0 $$api_pid >/dev/null 2>&1; then \
				if wait $$api_pid; then exit 1; else exit $$?; fi; \
			fi; \
			attempt=$$((attempt + 1)); \
			sleep 0.1; \
		done; \
		if [ $$ready -ne 1 ]; then \
			printf 'Steward API did not become ready\n' >&2; \
			exit 1; \
		fi; \
		STEWARD_DEV_PROXY_TOKEN=$$dev_token VITE_STEWARD_DEV_AUTO_LOGIN=1 npm --prefix web run dev

test:
	go test ./...
	npm --prefix web run test

lint:
	go vet ./...
	npm --prefix web run lint

format:
	go fmt ./...
	npm --prefix web run format
	rm -f web/tsconfig.tsbuildinfo
