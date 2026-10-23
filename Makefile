SHELL := /bin/sh
PYTHON ?= python3
MODULES := ./contracts/... ./libs/go/... ./services/api-gateway/... ./services/company-service/... ./services/news-service/... ./services/event-service/... ./services/analytics-service/... ./services/scoring-service/...
COMPOSE := docker compose --env-file .env -f infrastructure/docker/compose.yaml

.PHONY: setup up down collect test integration race vet fmt check e2e native-e2e logs contracts
setup:
	./scripts/setup.sh
up:
	$(COMPOSE) up --build --wait
down:
	$(COMPOSE) down
collect:
	$(COMPOSE) exec -T news-service /app collect
test:
	go test -count=1 $(MODULES)
integration:
	@test -n "$$TEST_DATABASE_URL" || (echo 'Set TEST_DATABASE_URL to a disposable PostgreSQL admin connection'; exit 1)
	go test -count=1 $(MODULES)
race:
	go test -race -count=1 $(MODULES)
vet:
	go vet $(MODULES)
fmt:
	gofmt -w $$(find contracts libs services -name '*.go')
check: vet test
	@test -z "$$(gofmt -l $$(find contracts libs services -name '*.go'))"
e2e:
	$(PYTHON) scripts/e2e.py
native-e2e:
	$(PYTHON) scripts/native-e2e.py
logs:
	$(COMPOSE) logs --tail=100 -f
contracts:
	$(PYTHON) scripts/check-contracts.py
