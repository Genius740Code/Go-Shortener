# makefile for url shortener project

.PHONY: run build test clean docker-build docker-run setup-db

# default target
all: build

# run the application locally
run:
	go run main.go

# build binary
build:
	go build -o bin/urlshortener main.go

# run tests (add test files later)
test:
	go test -v ./...

# clean build artifacts
clean:
	rm -rf bin/
	go clean

# setup local postgresql database
setup-db:
	createdb urlshortener || true
	psql urlshortener -c "CREATE TABLE IF NOT EXISTS urls (id SERIAL PRIMARY KEY, original_url TEXT NOT NULL, short_code VARCHAR(8) NOT NULL UNIQUE, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, click_count INTEGER DEFAULT 0);"
	psql urlshortener -c "CREATE INDEX IF NOT EXISTS idx_short_code ON urls(short_code);"
	psql urlshortener -c "CREATE INDEX IF NOT EXISTS idx_original_url ON urls(original_url);"

# install dependencies
deps:
	go mod download
	go mod tidy

# build docker image
docker-build:
	docker build -t urlshortener .

# run with docker
docker-run:
	docker run -p 8080:8080 --env DATABASE_URL="postgres://postgres:@host.docker.internal/urlshortener?sslmode=disable" urlshortener

# run with docker compose (if you create docker-compose.yml)
compose-up:
	docker-compose up -d

compose-down:
	docker-compose down

# format code
fmt:
	go fmt ./...

# run linter (install golangci-lint first)
lint:
	golangci-lint run

# show help
help:
	@echo "Available targets:"
	@echo "  run        - Run the application locally"
	@echo "  build      - Build binary"
	@echo "  test       - Run tests" 
	@echo "  clean      - Clean build artifacts"
	@echo "  setup-db   - Setup local postgresql database"
	@echo "  deps       - Install/update dependencies"
	@echo "  docker-build - Build docker image"
	@echo "  docker-run - Run with docker"
	@echo "  fmt        - Format code"
	@echo "  lint       - Run linter"