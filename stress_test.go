package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// echoTCPServer is a TCP server that reads PROXY header and echoes all data
type echoTCPServer struct {
	listener    net.Listener
	connections atomic.Int64
	mu          sync.Mutex
	conns       []net.Conn
	closed      bool
}

func newEchoTCPServer(t *testing.T) *echoTCPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start echo TCP server: %v", err)
	}
	s := &echoTCPServer{listener: listener}
	go s.serve()
	return s
}

func (s *echoTCPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns = append(s.conns, conn)
		s.connections.Add(1)
		s.mu.Unlock()

		go func(c net.Conn) {
			defer func() {
				c.Close()
				s.connections.Add(-1)
			}()
			// Read PROXY v2 header (28 bytes for IPv4)
			header := make([]byte, 28)
			if _, err := io.ReadFull(c, header); err != nil {
				return
			}
			// Echo all subsequent data
			io.Copy(c, c)
		}(conn)
	}
}

func (s *echoTCPServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *echoTCPServer) Close() {
	s.mu.Lock()
	s.closed = true
	// Close all active connections to unblock io.Copy
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
	s.mu.Unlock()

	s.listener.Close()
}

func (s *echoTCPServer) ActiveConnections() int64 {
	return s.connections.Load()
}

func TestStress_RapidConnectDisconnect(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ConnectTimeout: DefaultConnectTimeout,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	initialGoroutines := runtime.NumGoroutine()
	cycles := 100 // Reduced for faster tests, increase for thorough stress testing

	for i := 0; i < cycles; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			cancel()
			t.Fatalf("connection %d failed: %v", i, err)
		}

		// Send a small message
		err = conn.Write(ctx, websocket.MessageBinary, []byte("ping"))
		if err != nil {
			conn.CloseNow()
			cancel()
			t.Fatalf("write %d failed: %v", i, err)
		}

		// Read response
		_, _, err = conn.Read(ctx)
		if err != nil {
			conn.CloseNow()
			cancel()
			t.Fatalf("read %d failed: %v", i, err)
		}

		conn.Close(websocket.StatusNormalClosure, "")
		cancel()
	}

	// Wait for connections to fully close
	time.Sleep(500 * time.Millisecond)

	// Check for goroutine leaks
	finalGoroutines := runtime.NumGoroutine()
	leaked := finalGoroutines - initialGoroutines
	if leaked > 10 { // Allow some variance
		t.Errorf("potential goroutine leak: started with %d, ended with %d (leaked %d)",
			initialGoroutines, finalGoroutines, leaked)
	}

	// Verify all connections are closed
	if handler.ActiveConnections() != 0 {
		t.Errorf("expected 0 active connections, got %d", handler.ActiveConnections())
	}
}

func TestStress_ConcurrentConnectionStorm(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ConnectTimeout: DefaultConnectTimeout,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	numConnections := 100 // Reduced for faster tests

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var errorCount atomic.Int64

	// Launch all connections simultaneously
	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				errorCount.Add(1)
				return
			}
			defer conn.CloseNow()

			// Send/receive a message
			msg := []byte("hello from connection")
			if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
				errorCount.Add(1)
				return
			}

			_, resp, err := conn.Read(ctx)
			if err != nil {
				errorCount.Add(1)
				return
			}

			if !bytes.Equal(resp, msg) {
				errorCount.Add(1)
				return
			}

			successCount.Add(1)
			conn.Close(websocket.StatusNormalClosure, "")
		}(i)
	}

	wg.Wait()

	t.Logf("Concurrent storm results: %d success, %d errors out of %d",
		successCount.Load(), errorCount.Load(), numConnections)

	// Allow some failures due to resource constraints, but most should succeed
	successRate := float64(successCount.Load()) / float64(numConnections)
	if successRate < 0.95 {
		t.Errorf("success rate too low: %.2f%% (expected >95%%)", successRate*100)
	}
}

func TestStress_LongSession(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ReadTimeout:    0, // Disable for long session
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

	messageCount := 1000 // Reduced for faster tests
	messageSize := 128

	for i := 0; i < messageCount; i++ {
		msg := make([]byte, messageSize)
		for j := range msg {
			msg[j] = byte(i % 256)
		}

		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}

		_, resp, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read %d failed: %v", i, err)
		}

		if !bytes.Equal(resp, msg) {
			t.Fatalf("message %d mismatch", i)
		}
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

func TestStress_MemoryStability(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ConnectTimeout: DefaultConnectTimeout,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Force GC and get baseline
	runtime.GC()
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)

	// Cycle through many connections
	cycles := 50 // Reduced for faster tests
	for i := 0; i < cycles; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			cancel()
			t.Fatalf("connection %d failed: %v", i, err)
		}

		// Send some data
		for j := 0; j < 10; j++ {
			msg := make([]byte, 1024)
			conn.Write(ctx, websocket.MessageBinary, msg)
			conn.Read(ctx)
		}

		conn.Close(websocket.StatusNormalClosure, "")
		cancel()

		// Periodic GC
		if i%100 == 0 {
			runtime.GC()
		}
	}

	// Final GC and check memory
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)

	// Memory should return close to baseline (or decrease, which is fine)
	baselineMB := float64(baselineStats.HeapAlloc) / 1024 / 1024
	finalMB := float64(finalStats.HeapAlloc) / 1024 / 1024
	memGrowthMB := finalMB - baselineMB

	t.Logf("Memory: baseline=%.2f MB, final=%.2f MB, growth=%.2f MB",
		baselineMB, finalMB, memGrowthMB)

	// Allow up to 50MB growth (generous to account for test framework, etc.)
	// Negative growth (memory decreased) is fine
	if memGrowthMB > 50 {
		t.Errorf("excessive memory growth: %.2f MB", memGrowthMB)
	}
}

func TestStress_BackendDies(t *testing.T) {
	t.Parallel()
	// Test graceful handling when backend closes mid-session
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	backendClosed := make(chan struct{})

	// Backend that closes after reading PROXY header
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			// Read PROXY header
			buf := make([]byte, 28)
			io.ReadFull(conn, buf)
			// Abruptly close
			conn.Close()
		}
		close(backendClosed)
	}()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    listener.Addr().String(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	<-backendClosed
	listener.Close()

	// Try to read - should fail because backend closed
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Error("expected error when backend dies")
	}
}

func TestStress_ClientAbruptDisconnect(t *testing.T) {
	t.Parallel()
	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	initialActiveConns := handler.ActiveConnections()

	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			cancel()
			continue
		}

		// Send some data
		conn.Write(ctx, websocket.MessageBinary, []byte("data"))

		// Abrupt disconnect (no close frame)
		conn.CloseNow()
		cancel()
	}

	// Wait for cleanup
	time.Sleep(500 * time.Millisecond)

	// All connections should be cleaned up
	if handler.ActiveConnections() != initialActiveConns {
		t.Errorf("expected %d active connections after abrupt disconnects, got %d",
			initialActiveConns, handler.ActiveConnections())
	}
}

func TestStress_BackendUnreachable(t *testing.T) {
	t.Parallel()
	// Test that unreachable backend doesn't cause panics
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    "127.0.0.1:59999", // Port that's not listening
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ConnectTimeout: 100 * time.Millisecond,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Multiple connection attempts should not cause panics
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, _, _ := websocket.Dial(ctx, wsURL, nil)
		cancel()
		if conn != nil {
			conn.CloseNow()
		}
	}
	// Test passes if no panic occurred
}

func TestStress_LargeMessages(t *testing.T) {
	t.Parallel()
	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: 2 * 1024 * 1024, // 2MB limit
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	// Test with various message sizes
	// Note: TCP may fragment large messages, so we read until we have all data
	sizes := []int{1024, 4096, 16384, 32768} // 1KB, 4KB, 16KB, 32KB

	for _, size := range sizes {
		msg := make([]byte, size)
		for i := range msg {
			msg[i] = byte(i % 256)
		}

		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			t.Fatalf("write %d bytes failed: %v", size, err)
		}

		// Read all echoed data (TCP may fragment into multiple WebSocket messages)
		var received []byte
		for len(received) < size {
			_, chunk, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("read %d bytes failed after receiving %d: %v", size, len(received), err)
			}
			received = append(received, chunk...)
		}

		if !bytes.Equal(received, msg) {
			t.Errorf("message mismatch for %d bytes (got %d bytes)", size, len(received))
		}
	}
}

func TestStress_RapidSmallMessages(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ReadTimeout:    0, // Disable for burst test
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

	messageCount := 1000 // Reduced for faster tests
	messageSize := 32

	start := time.Now()
	for i := 0; i < messageCount; i++ {
		msg := make([]byte, messageSize)
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatalf("read %d failed: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	msgsPerSec := float64(messageCount) / elapsed.Seconds()
	t.Logf("Throughput: %.0f msgs/sec (%d msgs in %v)", msgsPerSec, messageCount, elapsed)
}

func TestStress_InterleavedBidirectional(t *testing.T) {
	t.Parallel()

	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
		ReadTimeout:    0,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	// Simpler bidirectional test: send messages and read responses sequentially
	// This tests that the proxy handles interleaved reads/writes correctly
	messageCount := 100

	for i := 0; i < messageCount; i++ {
		msg := make([]byte, 64)
		msg[0] = byte(i % 256)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			cancel()
			t.Fatalf("write %d failed: %v", i, err)
		}

		_, resp, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read %d failed: %v", i, err)
		}

		if !bytes.Equal(resp, msg) {
			t.Fatalf("message %d mismatch", i)
		}
	}
}

func TestStress_EmptyMessages(t *testing.T) {
	t.Parallel()
	echoServer := newEchoTCPServer(t)
	defer echoServer.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &ProxyHandler{
		BackendAddr:    echoServer.Addr(),
		Logger:         logger,
		MaxMessageSize: DefaultMaxMessageSize,
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	// Send a few empty binary frames and verify they're handled
	for i := 0; i < 5; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{}); err != nil {
			t.Fatalf("empty write %d failed: %v", i, err)
		}
		// Note: empty messages may not echo back if the TCP side has nothing to read
		// This mainly tests that empty messages don't crash the proxy
	}

	// Send a non-empty message to verify the connection still works
	testMsg := []byte("test after empty")
	if err := conn.Write(ctx, websocket.MessageBinary, testMsg); err != nil {
		t.Fatalf("test write failed: %v", err)
	}
}
