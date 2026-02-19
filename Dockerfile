FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/drps ./cmd/drps
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/drpc ./cmd/drpc

FROM alpine:3.21
RUN apk add --no-cache curl
COPY --from=builder /bin/drps /usr/local/bin/drps
COPY --from=builder /bin/drpc /usr/local/bin/drpc
