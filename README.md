# ws-proxy

WebSocket to TCP bridge with PROXY protocol v2 support.

## Purpose

Bridge WebSocket connections (from browsers) to raw TCP (game server), injecting PROXY protocol v2 headers so the backend sees real client IPs.

## Features

- Accept WebSocket connections on a configurable address
- Extract real client IP from `X-Forwarded-For` or `X-Real-IP` headers
- Connect to backend via raw TCP
- Send PROXY protocol v2 header with client's real IP before any data
- Bidirectional pipe: WebSocket binary frames ↔ TCP bytes
- Graceful shutdown on SIGTERM/SIGINT

## Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `LISTEN_ADDR` | WebSocket listen address | `:8080` |
| `BACKEND_ADDR` | TCP backend address (required) | - |
| `LOG_LEVEL` | Log level: debug/info/warn/error | `info` |

## Usage

### Run directly

```bash
BACKEND_ADDR=localhost:7171 ./ws-proxy
```

### Run with Docker

```bash
docker run -p 8080:8080 -e BACKEND_ADDR=gameserver:7171 ghcr.io/zippo/ws-proxy:latest
```

### Docker Compose

```yaml
services:
  ws-proxy:
    image: ghcr.io/zippo/ws-proxy:latest
    ports:
      - "8080:8080"
    environment:
      BACKEND_ADDR: gameserver:7171
      LOG_LEVEL: info
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
go test -v ./...
```

## License

MIT
