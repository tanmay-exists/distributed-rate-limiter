# --- Stage 1: Build Stage ---
FROM golang:alpine AS builder

# Install ca-certificates (needed for TLS/HTTPS calls)
RUN apk add --no-cache ca-certificates git

WORKDIR /app

# Cache Go modules dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary with CGO disabled for maximum portability
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o rate-limiter ./cmd/api

# --- Stage 2: Final Runtime Stage ---
FROM alpine:3.19

WORKDIR /app

# Copy ca-certificates and binary from builder stage
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/rate-limiter /app/rate-limiter

# Run as non-root user for security best practices
USER 10001:10001

EXPOSE 8080

ENTRYPOINT ["/app/rate-limiter"]
