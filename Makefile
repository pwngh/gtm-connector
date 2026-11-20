# gtm-connector: the Go connector and PostgreSQL.
#
#   make dev                  PostgreSQL in Compose, the connector on this machine
#   make up                   everything in Compose
#   make demo                 publish a card for each demo account that holds none
#   make down                 stop; the database and credentials are kept
#   make backup               back up the database and the credentials folder
#   make restore BACKUP=NAME  restore a backup into an empty database
#   make test                 go vet and the Go tests against the Compose database
#
# Add POSTGRES=18.7 to any of them to use the pinned fallback PostgreSQL, on
# its own volume. The first run writes .env (random passwords for the owner
# and app roles, your user IDs, and GTM_HOME, the folder holding
# credentials/ and backups/). Recipes never name a password: scripts read
# .env when they run, so `make -n` shows none.

SHELL := /bin/sh
.DEFAULT_GOAL := help

# Never let a toolchain line in go.mod download another Go.
export GOTOOLCHAIN := local
# Colima with a standalone docker-compose builds without BuildKit.
export DOCKER_BUILDKIT := 0
export COMPOSE_DOCKER_CLI_BUILD := 0

ifeq ($(POSTGRES),18.7)
export GTM_POSTGRES_IMAGE := postgres:18.7-alpine3.24@sha256:ff64c9e3bdf8e2120107d3559de8db314e8305f7e38c5105903c6295876738ff
export GTM_PG_VOLUME := pgdata18
endif

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif command -v docker-compose >/dev/null 2>&1; then echo docker-compose; fi)

# Only the non-secret settings are read here.
GTM_HOME = $(shell sed -n 's/^GTM_HOME=//p' .env 2>/dev/null)
GTM_PORT = $(shell sed -n 's/^GTM_PORT=//p' .env 2>/dev/null)
GTM_DB_PORT = $(shell sed -n 's/^GTM_DB_PORT=//p' .env 2>/dev/null)

WEB_SOURCES := $(shell find web/src web/public web/index.html web/vite.config.js web/package.json web/package-lock.json -type f 2>/dev/null)
GO_SOURCES := go.mod go.sum $(shell find cmd internal -name '*.go' -o -name '*.sql')

.PHONY: help env dev up down db demo backup restore test vet psql logs clean require-compose state

help: ## List the targets
	@awk 'BEGIN { FS = ":.*## " } /^[a-z-]+:.*## / { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

env:
	@scripts/init-env.sh

require-compose:
	@test -n "$(COMPOSE)" || { echo "Docker Compose was not found (docker compose or docker-compose)." >&2; exit 1; }

# Made here, before Compose mounts it, so Docker does not create it as root.
state: env
	@home="$$(sed -n 's/^GTM_HOME=//p' .env)"; mkdir -p "$$home/backups" && chmod 700 "$$home" "$$home/backups"

db: require-compose state ## Start PostgreSQL and make its port answer on 127.0.0.1
	$(COMPOSE) up -d --wait postgres
	@scripts/colima-forward.sh ensure $$(sed -n 's/^GTM_DB_PORT=//p' .env)

web/dist/index.html: $(WEB_SOURCES)
	cd web && npm ci --no-audit --no-fund && npm run build

build/gtm-connector: $(GO_SOURCES)
	go build -o $@ ./cmd/gtm-connector

dev: db web/dist/index.html build/gtm-connector ## Run the connector here, on PostgreSQL in Compose
	@$(COMPOSE) stop app >/dev/null 2>&1 || true
	@scripts/colima-forward.sh close $(GTM_PORT) >/dev/null 2>&1 || true
	scripts/host.sh setup
	@echo "Open https://127.0.0.1:$(GTM_PORT)/ (local certificate in $(GTM_HOME)/credentials/tls.pem)"
	exec scripts/host.sh serve

up: require-compose state ## Build and start PostgreSQL and the connector in Compose
	$(COMPOSE) build app
	$(COMPOSE) up -d --wait postgres
	$(COMPOSE) run --rm admin setup
	$(COMPOSE) up -d --wait app
	@scripts/colima-forward.sh ensure $(GTM_DB_PORT) $(GTM_PORT)
	@echo "Open https://127.0.0.1:$(GTM_PORT)/ (local certificate in $(GTM_HOME)/credentials/tls.pem)"

demo: db build/gtm-connector ## Publish a card for each demo account that holds none, for the demo sign-in
	scripts/host.sh setup
	scripts/host.sh demo

down: require-compose ## Stop everything; the database volume and credentials are kept
	$(COMPOSE) --profile tools down --remove-orphans
	@scripts/colima-forward.sh close $(GTM_DB_PORT) $(GTM_PORT) >/dev/null 2>&1 || true

backup: db ## Write GTM_HOME/backups/gtm-<time>.tar.gz
	$(COMPOSE) run --rm --no-deps admin backup --out /state/backups/gtm-$$(date -u +%Y%m%dT%H%M%SZ).tar.gz

restore: db ## Restore GTM_HOME/backups/$(BACKUP) into an empty database
	@test -n "$(BACKUP)" || { echo "usage: make restore BACKUP=<file name in $(GTM_HOME)/backups>" >&2; exit 2; }
	@$(COMPOSE) stop app >/dev/null 2>&1 || true
	$(COMPOSE) run --rm --no-deps admin restore --in '/state/backups/$(notdir $(BACKUP))'

vet: ## go vet
	go vet ./...

test: db vet ## The Go tests, with the race detector, against PostgreSQL in Compose
	scripts/host.sh test

psql: require-compose ## A database shell, as the owner
	$(COMPOSE) exec postgres psql -U gtm -d gtm

logs: require-compose ## Follow the connector's log
	$(COMPOSE) logs -f --tail 100 app

clean: require-compose ## Stop everything and DELETE the database volume (credentials and backups stay)
	$(COMPOSE) --profile tools down --volumes --remove-orphans
	@scripts/colima-forward.sh close $(GTM_DB_PORT) $(GTM_PORT) >/dev/null 2>&1 || true
