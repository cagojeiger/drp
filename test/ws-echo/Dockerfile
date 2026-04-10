FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /ws-echo .

FROM alpine:3.20
COPY --from=builder /ws-echo /usr/local/bin/ws-echo
ENTRYPOINT ["ws-echo"]
