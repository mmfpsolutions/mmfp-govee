# Multi-stage build for MMFP Govee
# CSS is pre-compiled by build scripts (build-local.sh)
# All web assets are embedded in the Go binary via go:embed

# Build arguments for version injection
ARG VERSION=dev
ARG BUILD_DATE=unknown
ARG COMMIT=unknown

# Builder stage
FROM golang:alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code (includes pre-compiled tailwind.css in internal/web/static/css/)
COPY . .

# Build arguments
ARG VERSION
ARG BUILD_DATE
ARG COMMIT

# Build the application with version injection
# All web assets (templates, JS, CSS, images) are embedded via go:embed
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
    -X 'github.com/mmfpsolutions/mmfp-govee/internal/version.Version=${VERSION}' \
    -X 'github.com/mmfpsolutions/mmfp-govee/internal/version.BuildDate=${BUILD_DATE}' \
    -X 'github.com/mmfpsolutions/mmfp-govee/internal/version.Commit=${COMMIT}'" \
    -o mmfp-govee ./cmd/mmfp-govee

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (su-exec for entrypoint user switching)
RUN apk --no-cache add ca-certificates tzdata su-exec wget

# Create app user
RUN addgroup -g 1000 app && \
    adduser -D -u 1000 -G app app

WORKDIR /app

# Copy binary from builder (all web assets are embedded in the binary)
COPY --from=builder /build/mmfp-govee .

# Copy entrypoint script
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Create config and logs directories with proper permissions
RUN mkdir -p /app/config /app/logs && chown -R app:app /app/config /app/logs

# Expose ports: web app + webhook listener
EXPOSE 3008 8787

# Health check (web app port)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:3008/health || exit 1

# Entrypoint fixes volume permissions then drops to app user
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["./mmfp-govee"]
