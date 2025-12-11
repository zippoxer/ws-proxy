# ws-proxy

WebSocket to TCP bridge with PROXY protocol v2 support.

## Purpose

Bridge WebSocket connections (from browsers) to raw TCP (game server), injecting PROXY protocol v2 headers so the backend sees real client IPs.

## Features

- Accept WebSocket connections on a configurable address
- Extract real client IP from `X-Forwarded-For` or `X-Real-IP` headers (when trusted)
- Connect to backend via raw TCP
- Send PROXY protocol v2 header with client's real IP before any data
- Bidirectional pipe: WebSocket binary frames ↔ TCP bytes
- Graceful shutdown on SIGTERM/SIGINT
- Security hardened: IP spoofing protection, origin checking, configurable limits

## Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `LISTEN_ADDR` | WebSocket listen address | `:8080` |
| `BACKEND_ADDR` | TCP backend address (required) | - |
| `LOG_LEVEL` | Log level: debug/info/warn/error | `info` |
| `MAX_MESSAGE_SIZE` | Max WebSocket message size in bytes | `1048576` (1MB) |
| `CONNECT_TIMEOUT` | Backend TCP connect timeout | `5s` |
| `READ_TIMEOUT` | Idle read timeout (0 to disable) | `60s` |
| `WRITE_TIMEOUT` | Write timeout | `10s` |
| `MAX_CONNECTIONS` | Max concurrent connections (0 = unlimited) | `0` |
| `TRUST_PROXY_HEADERS` | Trust X-Forwarded-For/X-Real-IP headers | `false` |
| `ALLOWED_ORIGINS` | Comma-separated allowed WebSocket origins | (allow all) |

Timeouts use Go duration format: `5s`, `1m`, `500ms`, etc.

### Security Configuration

**TRUST_PROXY_HEADERS**: By default, the proxy uses the TCP connection's remote address. Set to `true` only when deployed behind a trusted reverse proxy (nginx, HAProxy, etc.) that sets `X-Forwarded-For` or `X-Real-IP` headers.

**ALLOWED_ORIGINS**: By default, all WebSocket origins are allowed (for non-browser game clients). Set to a comma-separated list of allowed origins (e.g., `https://game.example.com,https://staging.example.com`) to enable origin checking for browser-based clients.

## Usage

### Run directly

```bash
BACKEND_ADDR=localhost:7171 ./ws-proxy
```

### Run with Docker

```bash
docker run -p 8080:8080 -e BACKEND_ADDR=gameserver:7171 ghcr.io/zippoxer/ws-proxy:latest
```

### Docker Compose

```yaml
services:
  ws-proxy:
    image: ghcr.io/zippoxer/ws-proxy:latest
    ports:
      - "8080:8080"
    environment:
      BACKEND_ADDR: gameserver:7171
      LOG_LEVEL: info
      MAX_MESSAGE_SIZE: 1048576
      CONNECT_TIMEOUT: 5s
      READ_TIMEOUT: 60s
      WRITE_TIMEOUT: 10s
      MAX_CONNECTIONS: 0
      # Security options (uncomment as needed):
      # TRUST_PROXY_HEADERS: "true"  # Enable if behind nginx/HAProxy
      # ALLOWED_ORIGINS: "https://game.example.com"
```

## PROXY Protocol v2

The proxy sends a PROXY protocol v2 header to the backend before any data. For IPv4, the header is 28 bytes:

| Bytes | Description |
|-------|-------------|
| 0-11 | Signature: `\r\n\r\n\0\r\nQUIT\n` |
| 12 | Version + Command: `0x21` (v2, PROXY) |
| 13 | Family + Protocol: `0x11` (IPv4, TCP) |
| 14-15 | Address length: `0x000C` (12, big-endian) |
| 16-19 | Source IP (4 bytes, network order) |
| 20-23 | Dest IP (4 bytes, network order) |
| 24-25 | Source port (2 bytes, big-endian) |
| 26-27 | Dest port (2 bytes, big-endian) |

## Building

```bash
go build -o ws-proxy .
```

## Testing

```bash
# Run all tests
go test -v ./...

# Run with race detector
go test -race ./...

# Run benchmarks
go test -bench=. -benchmem -run=^$ ./...

# Run stress tests (longer timeout)
go test -v -run='Test.*Stress' -timeout 5m ./...
```

### Test Categories

- **Unit tests** (`proxyv2_test.go`): PROXY protocol v2 header building
- **Integration tests** (`proxy_test.go`): End-to-end WebSocket to TCP proxying
- **Limits tests** (`limits_test.go`): Configuration limits enforcement
- **Security tests** (`security_test.go`): Input validation, injection prevention
- **Stress tests** (`stress_test.go`): Load testing, connection churn, memory stability
- **Benchmarks** (`benchmark_test.go`): Latency and throughput measurements

## License

MIT
