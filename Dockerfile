FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /drps ./cmd/drps/

FROM alpine:3.20
COPY --from=builder /drps /usr/local/bin/drps
ENTRYPOINT ["drps"]
