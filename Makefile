SHELL := /bin/bash
.DEFAULT_GOAL := deps

# Optional classifier: none of these targets runs during build/dev/test.
.PHONY: laya-setup laya-start test-laya-unit test-laya
laya-setup:
	cd services/laya && uv sync --locked && uv run --no-sync python setup_model.py
laya-start:
	cd services/laya && if [ -n "$(LAYA_PYTHON)" ]; then "$(LAYA_PYTHON)" start.py; else uv run --no-sync python start.py; fi
test-laya-unit:
	cd services/laya && python3 -m unittest test_worker.py
test-laya:
	RELAY_LAYA_INTEGRATION=1 go test ./pkg/characterization -run TestLayaIntegration -count=1 -v -timeout=20m

FRONTEND_DIR := web
FRONTEND_HOST ?= 127.0.0.1
FRONTEND_PORT ?= 3030
DEV_FRONTEND_TARGET ?= http://$(FRONTEND_HOST):$(FRONTEND_PORT)
DEV_RELOAD_FILE ?= $(CURDIR)/tmp/air/backend-ready
DEV_BACKEND_ADDR ?= $(if $(RELAY_HTTP_ADDR),$(RELAY_HTTP_ADDR),:11730)
DEV_API_TARGET ?= $(if $(filter :%,$(DEV_BACKEND_ADDR)),http://127.0.0.1$(DEV_BACKEND_ADDR),http://$(DEV_BACKEND_ADDR))
AIR ?= $(shell if command -v air >/dev/null 2>&1; then command -v air; else printf "%s/bin/air" "$$(go env GOPATH)"; fi)
NODE_VERSION ?= 22.19.0

ifneq (,$(wildcard $(FRONTEND_DIR)/yarn.lock))
FRONTEND_DEV_CMD := yarn dev --host $(FRONTEND_HOST) --port $(FRONTEND_PORT)
else ifneq (,$(wildcard $(FRONTEND_DIR)/pnpm-lock.yaml))
FRONTEND_DEV_CMD := pnpm dev --host $(FRONTEND_HOST) --port $(FRONTEND_PORT)
else ifneq (,$(wildcard $(FRONTEND_DIR)/package-lock.json))
FRONTEND_DEV_CMD := npm run dev -- --host $(FRONTEND_HOST) --port $(FRONTEND_PORT)
else ifneq (,$(wildcard $(FRONTEND_DIR)/bun.lockb)$(wildcard $(FRONTEND_DIR)/bun.lock))
FRONTEND_DEV_CMD := bun run dev -- --host $(FRONTEND_HOST) --port $(FRONTEND_PORT)
else
FRONTEND_DEV_CMD := npm run dev -- --host $(FRONTEND_HOST) --port $(FRONTEND_PORT)
endif

NODE_SETUP = \
	if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 && [ "$$(node -v)" = "v$(NODE_VERSION)" ]; then \
		:; \
	elif [ -f "$$HOME/.nvm/nvm.sh" ]; then \
		. "$$HOME/.nvm/nvm.sh" && nvm use $(NODE_VERSION) >/dev/null; \
	else \
		echo "Node.js $(NODE_VERSION) and npm are required. Install that version globally or via nvm." >&2; \
		exit 1; \
	fi

ifneq (,$(wildcard .env))
# Do not export legacy keys into the process environment. The standalone
# preparation command preserves values without starting Relay or opening a DB.
LEGACY_ENV_KEYS := $(shell sed -nE 's/^[[:space:]]*(export[[:space:]]+)?(BOUNCER_[A-Z0-9_]+)[[:space:]]*=.*/\2/p' .env)
ifneq ($(strip $(LEGACY_ENV_KEYS)),)
$(error Legacy .env names detected. Run: go run ./cmd/relay-config .env)
endif
include .env
export
endif

.PHONY: deps web-build embed-ui build run install-dev-tools dev dev-go dev-frontend test fmt

deps:
	GOCACHE=/tmp/relay-gocache go mod download
	cd web && $(NODE_SETUP) && npm install

web-build:
	cd web && $(NODE_SETUP) && npm run generate

embed-ui:
	rm -rf internal/ui/dist
	mkdir -p internal/ui/dist
	cp -R web/.output/public/. internal/ui/dist/
	touch internal/ui/dist/.gitkeep

build: web-build embed-ui
	GOCACHE=/tmp/relay-gocache go build -o bin/relay ./cmd/relay

run: web-build embed-ui
	GOCACHE=/tmp/relay-gocache go run ./cmd/relay

install-dev-tools:
	go install github.com/air-verse/air@latest

dev:
	@set -e; \
	echo "Starting Go backend with Air on $(DEV_BACKEND_ADDR)"; \
	echo "Starting Nuxt dev server at $(DEV_FRONTEND_TARGET)"; \
	echo "Redirecting backend UI pages to $(DEV_FRONTEND_TARGET) for stable HMR"; \
	trap 'status=$$?; echo "Stopping development processes..."; kill $$go_pid $$frontend_pid 2>/dev/null || true; wait $$go_pid $$frontend_pid 2>/dev/null || true; exit $$status' INT TERM EXIT; \
	$(MAKE) dev-go & go_pid=$$!; \
	$(MAKE) dev-frontend & frontend_pid=$$!; \
	while true; do \
		if ! kill -0 $$go_pid 2>/dev/null; then wait $$go_pid; exit $$?; fi; \
		if ! kill -0 $$frontend_pid 2>/dev/null; then wait $$frontend_pid; exit $$?; fi; \
		sleep 1; \
	done

dev-go:
	APP_ENV=development RELAY_HTTP_ADDR="$(DEV_BACKEND_ADDR)" RELAY_DEV_UI_TARGET="$(DEV_FRONTEND_TARGET)" RELAY_DEV_RELOAD_FILE="$(DEV_RELOAD_FILE)" RELAY_INSECURE_DEV=true GOCACHE=/tmp/relay-gocache $(AIR) -c .air.toml

dev-frontend:
	cd $(FRONTEND_DIR) && $(NODE_SETUP) && NUXT_DEV_API_TARGET="$(DEV_API_TARGET)" NUXT_PUBLIC_RELAY_WS_TARGET="$(DEV_API_TARGET)" NUXT_DEV_BACKEND_RELOAD_FILE="$(DEV_RELOAD_FILE)" NUXT_DEV_HOST="$(FRONTEND_HOST)" NUXT_DEV_PORT="$(FRONTEND_PORT)" $(FRONTEND_DEV_CMD)

test:
	GOCACHE=/tmp/relay-gocache go test ./...

fmt:
	gofmt -w $$(find cmd internal pkg -name '*.go')
