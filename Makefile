# gtm-connector: the Go connector and PostgreSQL.
#
#   make dev                  PostgreSQL in Compose, the connector on this machine
#   make up                   everything in Compose
#   make down                 stop; the database and credentials are kept
#   make backup               back up the database and the credentials folder
#   make restore BACKUP=NAME  restore a backup into an empty database
#   make test                 go vet and the Go tests against the Compose database
#
# The first run writes .env (a random database password, your user IDs, and
# GTM_HOME, the folder holding credentials/ and backups/).

SHELL := /bin/sh
.DEFAULT_GOAL := help

# Never let a toolchain line in go.mod download another Go.
export GOTOOLCHAIN := local
# Colima with a standalone docker-compose builds without BuildKit.
export DOCKER_BUILDKIT := 0
export COMPOSE_DOCKER_CLI_BUILD := 0

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif command -v docker-compose >/dev/null 2>&1; then echo docker-compose; fi)

-include .env

HOST_ENV = GTM_DATABASE_URL='postgres://gtm:$(POSTGRES_PASSWORD)@127.0.0.1:$(GTM_DB_PORT)/gtm?sslmode=disable' \
	GTM_CREDENTIALS_DIR='$(GTM_HOME)/credentials' GTM_LISTEN='127.0.0.1:$(GTM_PORT)' \
	GTM_PUBLIC_ORIGIN='https://127.0.0.1:$(GTM_PORT)' \
	GTM_ALLOWED_HOSTS='127.0.0.1:$(GTM_PORT),localhost:$(GTM_PORT)' GTM_WEB_DIR=web/dist

.PHONY: help dev up down db backup restore test vet psql logs clean require-compose state

help: ## List the targets
	@awk 'BEGIN { FS = ":.*## " } /^[a-z-]+:.*## / { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.env:
	@scripts/init-env.sh

require-compose:
	@test -n "$(COMPOSE)" || { echo "Docker Compose was not found (docker compose or docker-compose)." >&2; exit 1; }

# Made here, before Compose mounts it, so Docker does not create it as root.
state: .env
	@mkdir -p '$(GTM_HOME)/backups' && chmod 700 '$(GTM_HOME)' '$(GTM_HOME)/backups'

db: require-compose state ## Start PostgreSQL and make its port answer on 127.0.0.1
	$(COMPOSE) up -d --wait postgres
	@scripts/colima-forward.sh ensure $(GTM_DB_PORT)

web/dist/index.html: web/package.json web/package-lock.json
	cd web && npm ci --no-audit --no-fund && npm run build

build/gtm-connector: go.mod go.sum $(shell find cmd internal -name '*.go' -o -name '*.sql')
	go build -o $@ ./cmd/gtm-connector

dev: db web/dist/index.html build/gtm-connector ## Run the connector here, on PostgreSQL in Compose
	@$(COMPOSE) stop app >/dev/null 2>&1 || true
	@scripts/colima-forward.sh close $(GTM_PORT) >/dev/null 2>&1 || true
	@echo "Open https://127.0.0.1:$(GTM_PORT)/ (local certificate in $(GTM_HOME)/credentials/tls.pem)"
	@# Not echoed: the environment carries the database password.
	@$(HOST_ENV) exec build/gtm-connector serve

up: require-compose state ## Build and start PostgreSQL and the connector in Compose
	$(COMPOSE) build app
	$(COMPOSE) up -d --wait
	@scripts/colima-forward.sh ensure $(GTM_DB_PORT) $(GTM_PORT)
	@echo "Open https://127.0.0.1:$(GTM_PORT)/ (local certificate in $(GTM_HOME)/credentials/tls.pem)"

down: require-compose ## Stop everything; the database volume and credentials are kept
	$(COMPOSE) down --remove-orphans
	@scripts/colima-forward.sh close $(GTM_DB_PORT) $(GTM_PORT) >/dev/null 2>&1 || true

backup: db ## Write GTM_HOME/backups/gtm-<time>.tar.gz
	$(COMPOSE) run --rm --no-deps app backup --out /state/backups/gtm-$$(date -u +%Y%m%dT%H%M%SZ).tar.gz

restore: db ## Restore GTM_HOME/backups/$(BACKUP) into an empty database
	@test -n "$(BACKUP)" || { echo "usage: make restore BACKUP=<file name in $(GTM_HOME)/backups>" >&2; exit 2; }
	@$(COMPOSE) stop app >/dev/null 2>&1 || true
	$(COMPOSE) run --rm --no-deps app restore --in '/state/backups/$(notdir $(BACKUP))'

vet: ## go vet
	go vet ./...

test: db vet ## The Go tests, with the race detector, against PostgreSQL in Compose
	@echo "go test -race ./... (on the Compose database)"
	@GTM_TEST_DATABASE_URL='postgres://gtm:$(POSTGRES_PASSWORD)@127.0.0.1:$(GTM_DB_PORT)/gtm?sslmode=disable' \
		GTM_REQUIRE_DATABASE=1 go test -race ./...

psql: require-compose ## A database shell
	$(COMPOSE) exec postgres psql -U gtm -d gtm

logs: require-compose ## Follow the connector's log
	$(COMPOSE) logs -f --tail 100 app

clean: require-compose ## Stop everything and DELETE the database volume (credentials and backups stay)
	$(COMPOSE) down --volumes --remove-orphans
	@scripts/colima-forward.sh close $(GTM_DB_PORT) $(GTM_PORT) >/dev/null 2>&1 || true
