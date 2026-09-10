.PHONY: dev ui build test compose-up compose-down

dev:
	BOOTSTRAP_ADMIN_PASSWORD=development-admin-password DEMO_DATA=true go run ./cmd/server

ui:
	npm run dev --prefix ui

build:
	npm run build --prefix ui
	go build -o bin/bce-server ./cmd/server
	go build -o bin/bce-agent ./cmd/agent

test:
	go test ./...
	npm run build --prefix ui

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

