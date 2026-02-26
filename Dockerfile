# ===================================
# Stage 1: Builder
# ===================================
FROM golang:1.23.4-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build


COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -a -installsuffix cgo \
    -o flashmeet .

# ===================================
# Stage 2: Runtime (Distroless)
# ===================================
FROM gcr.io/distroless/static-debian12:nonroot

# Copy timezone data and CA certificates
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binary and static files
COPY --from=builder /build/flashmeet /app/flashmeet
COPY --from=builder /build/index.html /app/index.html

WORKDIR /app

USER nonroot:nonroot

# Expose port
EXPOSE 8080

# Run the application
ENTRYPOINT ["/app/flashmeet"]