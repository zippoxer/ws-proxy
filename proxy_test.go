package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// mockTCPServer is a test TCP server that captures the PROXY v2 header and echoes data.
type mockTCPServer struct {
	listener     net.Listener
	proxyHeader  []byte
	receivedData []byte
	mu           sync.Mutex
	connected    chan struct{}
}

func newMockTCPServer(t *testing.T) *mockTCPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP server: %v", err)
	}
	return &mockTCPServer{
		listener:  listener,
		connected: make(chan struct{}),
	}
}

func (m *mockTCPServer) Addr() string {
	return m.listener.Addr().String()
}

func (m *mockTCPServer) Close() {
	m.listener.Close()
}

func (m *mockTCPServer) Accept(t *testing.T) {
	t.Helper()
	conn, err := m.listener.Accept()
	if err != nil {
		t.Logf("accept error: %v", err)
		return
	}
	defer conn.Close()

	// Read PROXY v2 header first
	header := make([]byte, 28) // IPv4 header size
	_, err = io.ReadFull(conn, header)
	if err != nil {
		t.Logf("failed to read proxy header: %v", err)
		return
	}

	m.mu.Lock()
	m.proxyHeader = header
	m.mu.Unlock()

	close(m.connected)

	// Echo any received data
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		m.mu.Lock()
		m.receivedData = append(m.receivedData, buf[:n]...)
		m.mu.Unlock()
		// Echo back
		conn.Write(buf[:n])
	}
}

func (m *mockTCPServer) GetProxyHeader() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proxyHeader
}

func (m *mockTCPServer) GetReceivedData() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receivedData
}

func TestIntegration_WebSocketToTCP(t *testing.T) {
	// Start mock TCP server
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	// Create proxy handler
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr: mockServer.Addr(),
		Logger:      logger,
	}

	// Create test HTTP server with the proxy handler
	server := httptest.NewServer(handler)
	defer server.Close()

	// Convert HTTP URL to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Connect via WebSocket with X-Forwarded-For header
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	headers := http.Header{}
	headers.Set("X-Forwarded-For", "203.0.113.50")

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("failed to connect via websocket: %v", err)
	}
	defer conn.CloseNow()

	// Wait for mock server to receive connection
	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Send test data
	testData := []byte("Hello, game server!")
	err = conn.Write(ctx, websocket.MessageBinary, testData)
	if err != nil {
		t.Fatalf("failed to write to websocket: %v", err)
	}

	// Read echoed response
	msgType, response, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read from websocket: %v", err)
	}

	if msgType != websocket.MessageBinary {
		t.Errorf("expected binary message, got %v", msgType)
	}

	if !bytes.Equal(response, testData) {
		t.Errorf("response mismatch:\nexpected: %q\ngot:      %q", testData, response)
	}

	// Verify PROXY v2 header
	proxyHeader := mockServer.GetProxyHeader()
	if len(proxyHeader) != 28 {
		t.Fatalf("expected 28-byte proxy header, got %d bytes", len(proxyHeader))
	}

	// Verify signature
	expectedSig := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	if !bytes.Equal(proxyHeader[0:12], expectedSig) {
		t.Errorf("proxy header signature mismatch")
	}

	// Verify version + command
	if proxyHeader[12] != 0x21 {
		t.Errorf("expected version+command 0x21, got 0x%02x", proxyHeader[12])
	}

	// Verify family + protocol (IPv4 TCP)
	if proxyHeader[13] != 0x11 {
		t.Errorf("expected family+protocol 0x11, got 0x%02x", proxyHeader[13])
	}

	// Verify source IP is 203.0.113.50
	srcIP := net.IP(proxyHeader[16:20])
	expectedIP := net.ParseIP("203.0.113.50").To4()
	if !bytes.Equal(srcIP, expectedIP) {
		t.Errorf("source IP mismatch:\nexpected: %v\ngot:      %v", expectedIP, srcIP)
	}

	conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestIntegration_XRealIP(t *testing.T) {
	// Start mock TCP server
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	// Create proxy handler
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr: mockServer.Addr(),
		Logger:      logger,
	}

	// Create test HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use X-Real-IP header instead
	headers := http.Header{}
	headers.Set("X-Real-IP", "198.51.100.25")

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("failed to connect via websocket: %v", err)
	}
	defer conn.CloseNow()

	// Wait for mock server to receive connection
	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Verify source IP in PROXY header
	proxyHeader := mockServer.GetProxyHeader()
	srcIP := net.IP(proxyHeader[16:20])
	expectedIP := net.ParseIP("198.51.100.25").To4()
	if !bytes.Equal(srcIP, expectedIP) {
		t.Errorf("source IP mismatch:\nexpected: %v\ngot:      %v", expectedIP, srcIP)
	}

	conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestIntegration_MultipleXForwardedFor(t *testing.T) {
	// Start mock TCP server
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	// Create proxy handler
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr: mockServer.Addr(),
		Logger:      logger,
	}

	// Create test HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// X-Forwarded-For with multiple IPs - should use the first one
	headers := http.Header{}
	headers.Set("X-Forwarded-For", "192.0.2.1, 10.0.0.1, 172.16.0.1")

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("failed to connect via websocket: %v", err)
	}
	defer conn.CloseNow()

	// Wait for mock server to receive connection
	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Verify source IP is the first one (192.0.2.1)
	proxyHeader := mockServer.GetProxyHeader()
	srcIP := net.IP(proxyHeader[16:20])
	expectedIP := net.ParseIP("192.0.2.1").To4()
	if !bytes.Equal(srcIP, expectedIP) {
		t.Errorf("source IP mismatch:\nexpected: %v\ngot:      %v", expectedIP, srcIP)
	}

	conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestIntegration_BidirectionalData(t *testing.T) {
	// Start mock TCP server
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	// Create proxy handler
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr: mockServer.Addr(),
		Logger:      logger,
	}

	// Create test HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect via websocket: %v", err)
	}
	defer conn.CloseNow()

	// Wait for mock server to receive connection
	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Send multiple messages and verify echo
	messages := [][]byte{
		[]byte("First message"),
		[]byte("Second message with more data"),
		{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE}, // Binary data
	}

	for i, msg := range messages {
		err = conn.Write(ctx, websocket.MessageBinary, msg)
		if err != nil {
			t.Fatalf("failed to write message %d: %v", i, err)
		}

		_, response, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("failed to read response %d: %v", i, err)
		}

		if !bytes.Equal(response, msg) {
			t.Errorf("message %d mismatch:\nexpected: %x\ngot:      %x", i, msg, response)
		}
	}

	conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestExtractClientIP(t *testing.T) {
	handler := &ProxyHandler{
		BackendAddr: "127.0.0.1:7171",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		expected   string
	}{
		{
			name:       "X-Forwarded-For single",
			xff:        "203.0.113.50",
			xri:        "",
			remoteAddr: "127.0.0.1:12345",
			expected:   "203.0.113.50",
		},
		{
			name:       "X-Forwarded-For multiple",
			xff:        "203.0.113.50, 10.0.0.1, 192.168.1.1",
			xri:        "",
			remoteAddr: "127.0.0.1:12345",
			expected:   "203.0.113.50",
		},
		{
			name:       "X-Real-IP",
			xff:        "",
			xri:        "198.51.100.25",
			remoteAddr: "127.0.0.1:12345",
			expected:   "198.51.100.25",
		},
		{
			name:       "X-Forwarded-For takes precedence",
			xff:        "203.0.113.50",
			xri:        "198.51.100.25",
			remoteAddr: "127.0.0.1:12345",
			expected:   "203.0.113.50",
		},
		{
			name:       "Fallback to RemoteAddr",
			xff:        "",
			xri:        "",
			remoteAddr: "192.168.1.100:54321",
			expected:   "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header:     make(http.Header),
				RemoteAddr: tt.remoteAddr,
			}
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			got := handler.extractClientIP(req)
			if got != tt.expected {
				t.Errorf("extractClientIP() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestProxyHeaderDestination(t *testing.T) {
	// Start mock TCP server
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	// Create proxy handler
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr: mockServer.Addr(),
		Logger:      logger,
	}

	// Create test HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect via websocket: %v", err)
	}
	defer conn.CloseNow()

	// Wait for mock server to receive connection
	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Verify destination port in PROXY header matches backend
	proxyHeader := mockServer.GetProxyHeader()
	dstPort := binary.BigEndian.Uint16(proxyHeader[26:28])

	_, expectedPortStr, _ := net.SplitHostPort(mockServer.Addr())
	var expectedPort uint16
	_, _ = strings.NewReader(expectedPortStr).Read(nil)
	// Parse port manually
	for _, c := range expectedPortStr {
		expectedPort = expectedPort*10 + uint16(c-'0')
	}

	if dstPort != expectedPort {
		t.Errorf("destination port mismatch: expected %d, got %d", expectedPort, dstPort)
	}

	conn.Close(websocket.StatusNormalClosure, "test complete")
}
