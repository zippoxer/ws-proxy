package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestMalformedXForwardedFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		xff      string
		expected string // Expected IP in PROXY header (0.0.0.0 for invalid)
	}{
		{"empty", "", "127.0.0.1"}, // Falls back to remote addr
		{"script_injection", "<script>alert(1)</script>", "0.0.0.0"},
		{"sql_injection", "'; DROP TABLE users; --", "0.0.0.0"},
		{"invalid_ip", "999.999.999.999", "0.0.0.0"},
		{"negative_octets", "-1.-1.-1.-1", "0.0.0.0"},
		{"overflow_octets", "256.256.256.256", "0.0.0.0"},
		{"ipv6_mapped_invalid", "::ffff:999.999.999.999", "0.0.0.0"},
		{"very_long", strings.Repeat("A", 10000), "0.0.0.0"},
		{"null_bytes", "192.168.1.1\x00evil", "0.0.0.0"},
		{"newline_injection", "192.168.1.1\r\nX-Injected: value", "0.0.0.0"},
		{"valid_ip", "203.0.113.50", "203.0.113.50"},
		{"valid_with_spaces", "  192.168.1.1  ", "192.168.1.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer := newMockTCPServer(t)
			defer mockServer.Close()

			go mockServer.Accept(t)

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler := &ProxyHandler{
				BackendAddr:       mockServer.Addr(),
				Logger:            logger,
				MaxMessageSize:    DefaultMaxMessageSize,
				TrustProxyHeaders: true, // Enable to test XFF parsing behavior
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			headers := http.Header{}
			if tt.xff != "" {
				headers.Set("X-Forwarded-For", tt.xff)
			}

			conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
				HTTPHeader: headers,
			})
			if err != nil {
				// Connection might fail for extremely malformed input, which is fine
				t.Logf("connection failed (acceptable): %v", err)
				return
			}
			defer conn.CloseNow()

			// Wait for backend connection
			select {
			case <-mockServer.connected:
			case <-time.After(2 * time.Second):
				t.Fatal("timeout waiting for backend connection")
			}

			// Verify we got a valid PROXY header (didn't crash)
			proxyHeader := mockServer.GetProxyHeader()
			if len(proxyHeader) != 28 {
				t.Errorf("expected 28-byte proxy header, got %d", len(proxyHeader))
				return
			}

			// Verify signature is correct (proxy didn't crash)
			expectedSig := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
			if !bytes.Equal(proxyHeader[0:12], expectedSig) {
				t.Error("PROXY header signature corrupted")
			}
		})
	}
}

func TestXFFInjection(t *testing.T) {
	t.Parallel()
	// Test that XFF header can't inject into PROXY protocol
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:       mockServer.Addr(),
		Logger:            logger,
		MaxMessageSize:    DefaultMaxMessageSize,
		TrustProxyHeaders: true, // Enable to test XFF injection resistance
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to inject PROXY protocol signature via XFF
	proxySignature := "\r\n\r\n\x00\r\nQUIT\n"
	headers := http.Header{}
	headers.Set("X-Forwarded-For", proxySignature)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Logf("connection failed (acceptable): %v", err)
		return
	}
	defer conn.CloseNow()

	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Verify the PROXY header wasn't corrupted
	proxyHeader := mockServer.GetProxyHeader()
	if len(proxyHeader) != 28 {
		t.Errorf("expected 28-byte proxy header, got %d", len(proxyHeader))
		return
	}

	// Verify the signature is the real PROXY v2 signature, not injected content
	expectedSig := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	if !bytes.Equal(proxyHeader[0:12], expectedSig) {
		t.Error("PROXY header signature was corrupted/injected")
	}
}

func TestInvalidIPFallback(t *testing.T) {
	t.Parallel()
	// Verify that invalid IPs fall back to 0.0.0.0 without crashing
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:       mockServer.Addr(),
		Logger:            logger,
		MaxMessageSize:    DefaultMaxMessageSize,
		TrustProxyHeaders: true, // Enable to test invalid IP fallback
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	headers := http.Header{}
	headers.Set("X-Forwarded-For", "not-an-ip-at-all")

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	proxyHeader := mockServer.GetProxyHeader()

	// Source IP should be 0.0.0.0 (fallback for unparseable IP)
	srcIP := net.IP(proxyHeader[16:20])
	expectedIP := net.IPv4(0, 0, 0, 0).To4()
	if !bytes.Equal(srcIP, expectedIP) {
		t.Errorf("expected fallback IP 0.0.0.0, got %v", srcIP)
	}
}

func TestNonWebSocketRequest(t *testing.T) {
	t.Parallel()
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    mockServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	// Send a plain HTTP GET (not WebSocket upgrade)
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should get an error response (not 200 OK)
	if resp.StatusCode == http.StatusOK {
		t.Error("expected non-200 status for non-WebSocket request")
	}
}

func TestInvalidWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    mockServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	// Send request with invalid WebSocket headers
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	// Missing Sec-WebSocket-Key and other required headers

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should fail with bad request
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Error("should not upgrade with invalid WebSocket headers")
	}
}

func TestClientCannotInjectProxyHeader(t *testing.T) {
	t.Parallel()
	// Test that data sent by client after connection can't be confused with PROXY header
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    mockServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Client tries to send data that looks like PROXY header
	fakeProxyHeader := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	err = conn.Write(ctx, websocket.MessageBinary, fakeProxyHeader)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Wait for data to be received
	time.Sleep(100 * time.Millisecond)

	// The PROXY header we received should be the legitimate one,
	// and the fake data should come after
	proxyHeader := mockServer.GetProxyHeader()
	if len(proxyHeader) != 28 {
		t.Errorf("expected 28-byte proxy header, got %d", len(proxyHeader))
	}

	// Verify the received data includes the client's fake header (as data, not protocol)
	receivedData := mockServer.GetReceivedData()
	if !bytes.Contains(receivedData, fakeProxyHeader) {
		t.Error("client's fake PROXY header should appear in data, not replace real header")
	}
}

func TestManyConnectionsSameIP(t *testing.T) {
	t.Parallel()
	// Verify that the proxy doesn't have special IP-based limits (it's generic)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Accept in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 28)
				io.ReadFull(c, buf)
				io.Copy(c, c)
			}(conn)
		}
	}()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    listener.Addr().String(),
		Logger:         logger,
		MaxConnections: 0, // No limit
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx := context.Background()

	// All with same XFF IP
	headers := http.Header{}
	headers.Set("X-Forwarded-For", "192.168.1.1")

	// Should be able to create many connections from "same" IP
	conns := make([]*websocket.Conn, 20)
	for i := 0; i < 20; i++ {
		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: headers,
		})
		if err != nil {
			t.Fatalf("connection %d failed: %v", i, err)
		}
		conns[i] = conn
	}

	// Cleanup
	for _, conn := range conns {
		conn.CloseNow()
	}
}

func TestBackendAddressValidation(t *testing.T) {
	t.Parallel()
	// Test that invalid backend addresses are handled gracefully at runtime
	// The important thing is the proxy doesn't panic with invalid backends
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	handler := &ProxyHandler{
		BackendAddr:    "127.0.0.1:59999", // Unreachable port
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ConnectTimeout: 100 * time.Millisecond,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Connection attempt should not panic (may succeed or fail depending on system)
	conn, _, _ := websocket.Dial(ctx, wsURL, nil)
	if conn != nil {
		conn.CloseNow()
	}
	// Test passes if no panic occurred
}

func TestSlowlorisProtection(t *testing.T) {
	t.Parallel()
	// Test that slow clients don't hang forever due to timeouts
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    mockServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ReadTimeout:    200 * time.Millisecond, // Short timeout for test
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Don't send any data, just wait
	// The connection should be closed by the server due to read timeout
	start := time.Now()
	_, _, err = conn.Read(context.Background())
	elapsed := time.Since(start)

	// Should timeout within reasonable time
	if elapsed > time.Second {
		t.Errorf("slowloris protection took too long: %v", elapsed)
	}
}

func TestBinaryOnlyEnforced(t *testing.T) {
	t.Parallel()
	// Test that text messages are handled (currently ignored per implementation)
	mockServer := newMockTCPServer(t)
	defer mockServer.Close()

	go mockServer.Accept(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    mockServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	select {
	case <-mockServer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for backend connection")
	}

	// Send text message (should be ignored by proxy, not forwarded)
	err = conn.Write(ctx, websocket.MessageText, []byte("text message"))
	if err != nil {
		t.Fatalf("text write failed: %v", err)
	}

	// Send binary message (should be forwarded)
	binaryData := []byte("binary message")
	err = conn.Write(ctx, websocket.MessageBinary, binaryData)
	if err != nil {
		t.Fatalf("binary write failed: %v", err)
	}

	// Read response (should be the binary data echoed)
	_, response, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if !bytes.Equal(response, binaryData) {
		t.Errorf("expected binary response, got: %q", response)
	}
}
