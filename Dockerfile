FROM golang:1.25-alpine AS builder

# gcc needed for CGO (sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=1 GOOS=linux go build -o self-improvement-mcp .

# ── Runtime image ─────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/self-improvement-mcp .

RUN mkdir -p /data /config

EXPOSE 8080

# Default: look for config at /config/config.toml
# Override with CONFIG_FILE env var or --config flag
ENV CONFIG_FILE=/config/config.toml

ENTRYPOINT ["./self-improvement-mcp"]
