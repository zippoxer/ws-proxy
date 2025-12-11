# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ws-proxy .

# Runtime stage
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /app/ws-proxy /ws-proxy

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/ws-proxy"]
