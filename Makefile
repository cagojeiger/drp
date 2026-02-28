export GO111MODULE=on

LDFLAGS := -s -w
BINARY_DIR := bin
PROTO_DIR := proto
PROTO_FILES := $(shell find $(PROTO_DIR) -name '*.proto' 2>/dev/null)

.PHONY: all build clean test lint fmt vet proto proto-check drps drpc

all: proto fmt vet lint build test

# Protobuf
proto:
	@which protoc-gen-go > /dev/null || (echo "Run: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest" && exit 1)
	protoc \
		--proto_path=. \
		--go_out=. \
		--go_opt=paths=source_relative \
		$(PROTO_FILES)

proto-check: proto
	@git diff --exit-code $(PROTO_DIR) || (echo "Generated proto files are out of date. Run 'make proto' and commit." && exit 1)

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
	go test -v -race -count=1 ./internal/...
	go test -v -race -count=1 ./test/e2e/...

# Clean
clean:
	rm -rf $(BINARY_DIR)
