package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"nhooyr.io/websocket"
)

// ProxyHandler handles WebSocket to TCP proxying with PROXY protocol v2 support.
type ProxyHandler struct {
	BackendAddr string
	Logger      *slog.Logger
}

// ServeHTTP handles incoming WebSocket connections and proxies them to the backend.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract client IP from headers or remote address
	clientIP := p.extractClientIP(r)
	logger := p.Logger.With("client_ip", clientIP, "backend", p.BackendAddr)

	logger.Debug("incoming connection", "remote_addr", r.RemoteAddr)

	// Accept WebSocket connection
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow all origins for game clients
		InsecureSkipVerify: true,
	})
	if err != nil {
		logger.Error("failed to accept websocket", "error", err)
		return
	}
	defer conn.CloseNow()

	logger.Info("websocket connection accepted")

	// Connect to backend TCP server
	tcpConn, err := net.Dial("tcp", p.BackendAddr)
	if err != nil {
		logger.Error("failed to connect to backend", "error", err)
		conn.Close(websocket.StatusInternalError, "backend unavailable")
		return
	}
	defer tcpConn.Close()

	logger.Debug("connected to backend")

	// Build and send PROXY protocol v2 header
	if err := p.sendProxyHeader(tcpConn, clientIP, logger); err != nil {
		logger.Error("failed to send proxy header", "error", err)
		conn.Close(websocket.StatusInternalError, "proxy header failed")
		return
	}

	logger.Debug("proxy header sent")

	// Create context for managing goroutines
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Bidirectional pipe
	var wg sync.WaitGroup
	wg.Add(2)

	// WebSocket -> TCP
	go func() {
		defer wg.Done()
		defer cancel()
		if err := p.wsToTCP(ctx, conn, tcpConn); err != nil {
			if !isExpectedCloseError(err) {
				logger.Debug("ws->tcp error", "error", err)
			}
		}
	}()

	// TCP -> WebSocket
	go func() {
		defer wg.Done()
		defer cancel()
		if err := p.tcpToWS(ctx, tcpConn, conn); err != nil {
			if !isExpectedCloseError(err) {
				logger.Debug("tcp->ws error", "error", err)
			}
		}
	}()

	wg.Wait()
	logger.Info("connection closed")
}

// extractClientIP gets the real client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr.
func (p *ProxyHandler) extractClientIP(r *http.Request) string {
	// Check X-Forwarded-For first (may contain multiple IPs, take the first)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can be "client, proxy1, proxy2"
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}

	// Check X-Real-IP
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sendProxyHeader builds and sends the PROXY protocol v2 header to the backend.
func (p *ProxyHandler) sendProxyHeader(tcpConn net.Conn, clientIP string, logger *slog.Logger) error {
	// Parse client IP
	srcIP := net.ParseIP(clientIP)
	if srcIP == nil {
		// If we can't parse the IP, use a placeholder
		srcIP = net.IPv4(0, 0, 0, 0)
		logger.Warn("could not parse client IP, using placeholder", "client_ip", clientIP)
	}

	// Get backend IP and port
	dstIP, dstPort, err := ParseIPPort(p.BackendAddr)
	if err != nil {
		return err
	}

	// Use a synthetic source port (we don't have the real one from the load balancer)
	srcPort := uint16(0)

	// Build header
	header, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
	if err != nil {
		return err
	}

	// Send header
	_, err = tcpConn.Write(header)
	return err
}

// wsToTCP reads binary messages from WebSocket and writes to TCP.
func (p *ProxyHandler) wsToTCP(ctx context.Context, wsConn *websocket.Conn, tcpConn net.Conn) error {
	for {
		msgType, data, err := wsConn.Read(ctx)
		if err != nil {
			return err
		}

		// Only handle binary messages for game traffic
		if msgType != websocket.MessageBinary {
			p.Logger.Debug("ignoring non-binary message", "type", msgType)
			continue
		}

		_, err = tcpConn.Write(data)
		if err != nil {
			return err
		}
	}
}

// tcpToWS reads from TCP and writes binary messages to WebSocket.
func (p *ProxyHandler) tcpToWS(ctx context.Context, tcpConn net.Conn, wsConn *websocket.Conn) error {
	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := tcpConn.Read(buf)
		if err != nil {
			return err
		}

		if err := wsConn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
			return err
		}
	}
}

// isExpectedCloseError returns true if the error is an expected connection close.
func isExpectedCloseError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return true
	}
	// Check for websocket close errors
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return true
	}
	// Check for network errors that indicate clean shutdown
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "connection reset by peer")
}
