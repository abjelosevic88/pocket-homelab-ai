VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: all web build run dev test lint install service clean help

all: web build

web:                       ## build the React UI into web/dist
	cd web && npm ci --no-audit --no-fund && npm run build

build:                     ## build the Go binary (embeds web/dist)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o pocket-ai ./cmd/pocket-ai

run: build                 ## run locally on 127.0.0.1:8195 without a login (loopback only)
	DATA_DIR=./data AUTH_MODE=none HOST=127.0.0.1 LOG_LEVEL=debug ./pocket-ai

dev:                       ## backend on :8195 + vite dev server on :5173 with hot reload
	@echo "Starting API on :8195 and Vite on :5173 (open http://localhost:5173)"
	@(DATA_DIR=./data AUTH_MODE=none HOST=127.0.0.1 LOG_LEVEL=debug go run ./cmd/pocket-ai &) && cd web && PAI_API=http://127.0.0.1:8195 npm run dev

test:                      ## run Go tests and type-check the UI
	go vet ./... && go test ./...
	cd web && npx tsc --noEmit

lint:
	gofmt -l . && go vet ./...

install: all               ## install the binary to $(PREFIX)/bin
	install -Dm755 pocket-ai $(PREFIX)/bin/pocket-ai

service:                   ## install and start the systemd user unit (edit ~/.config/pocket-ai/env first)
	install -Dm644 deploy/pocket-ai.service $(HOME)/.config/systemd/user/pocket-ai.service
	@test -f $(HOME)/.config/pocket-ai/env || install -Dm600 .env.example $(HOME)/.config/pocket-ai/env
	systemctl --user daemon-reload && systemctl --user enable --now pocket-ai

clean:
	rm -rf pocket-ai web/dist/assets web/dist/index.html web/node_modules dist

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
