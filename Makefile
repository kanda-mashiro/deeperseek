.PHONY: test build dev-backend dev-frontend

test:
	cd backend && go test ./...
	cd frontend && npm run build

e2e:
	cd frontend && npm run e2e

build:
	cd backend && go build ./cmd/server
	cd frontend && npm run build

dev-backend:
	cd backend && go run ./cmd/server

dev-frontend:
	cd frontend && npm run dev
