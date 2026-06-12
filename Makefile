# Local development entry points. `make up run` brings the whole stack up.

.PHONY: run test test-integration lint up down

run: ## Start the server (expects env vars; see .env.example)
	go run ./cmd/server

test: ## Vet + unit tests (integration tests skip themselves without TEST_DATABASE_URL)
	go vet ./...
	go test ./...

test-integration: ## Full test run against a real Postgres (make up first)
	@test -n "$(TEST_DATABASE_URL)" || { \
		echo "TEST_DATABASE_URL is not set, e.g."; \
		echo "  export TEST_DATABASE_URL=postgres://molotlite:molotlite@localhost:5432/molotlite?sslmode=disable"; \
		exit 1; }
	go test ./...

lint: ## golangci-lint (govet, staticcheck, errcheck)
	golangci-lint run

up: ## Start local Postgres and wait until healthy
	docker compose up -d --wait

down: ## Stop local Postgres and drop its data
	docker compose down -v
