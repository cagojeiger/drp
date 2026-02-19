export GO111MODULE=on

LDFLAGS := -s -w
BINARY_DIR := bin

.PHONY: all build clean test lint e2e alltest fmt vet docker-build docker-e2e drps drpc

all: fmt vet lint build test

# Build
build: drps drpc

drps:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/drps ./cmd/drps

drpc:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/drpc ./cmd/drpc

# Quality
fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Test
test:
	go test -v -race -cover ./internal/...
	go test -v -race -cover ./test/e2e/...

e2e: build docker-build
	docker compose -f test/docker-compose.yml up --build --abort-on-container-exit --exit-code-from test
	docker compose -f test/docker-compose.yml down -v

alltest: fmt vet lint test e2e

# Docker
docker-build:
	docker build -t drp:test .

docker-e2e: docker-build
	docker compose -f test/docker-compose.yml up --build --abort-on-container-exit --exit-code-from test
	docker compose -f test/docker-compose.yml down -v

# Clean
clean:
	rm -rf $(BINARY_DIR)
	docker compose -f test/docker-compose.yml down -v 2>/dev/null || true
