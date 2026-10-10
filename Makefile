.PHONY: setup up down dev build test unit integration lint generate e2e test-db
setup:
	go run ./cmd/setup
up:
	docker compose up --build -d --wait
down:
	docker compose down
dev:
	go run ./cmd/sherlock
build:
	cd web && npm ci && npm run build
	go build -o bin/sherlock ./cmd/sherlock
unit:
	go test -race ./...
test-db:
	docker compose -f compose.test.yaml up -d --wait
integration: test-db
	SHERLOCK_TEST_DATABASE_URL='postgres://sherlock_test:local-test-only@127.0.0.1:55432/sherlock_test?sslmode=disable' go test -race -count=1 ./internal/api ./migrations
test: unit integration
lint:
	test -z "$$(gofmt -l cmd internal migrations)"
	go vet ./...
	cd web && npm run lint
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
e2e: test-db
	cd web && npm run build && npm run e2e

demo:
	docker compose --profile demo up --build -d --wait

real-sources:
	python3 scripts/real_sources.py
