package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nhooyr.io/websocket"
)

func BenchmarkProxyV2HeaderBuild(b *testing.B) {
	srcIP := net.ParseIP("192.168.1.100")
	dstIP := net.ParseIP("10.0.0.1")
	srcPort := uint16(54321)
	dstPort := uint16(7171)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProxyV2HeaderBuild_IPv6(b *testing.B) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	srcPort := uint16(54321)
	dstPort := uint16(7171)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// benchEchoServer for benchmarks
type benchEchoServer struct {
	listener net.Listener
	wg       sync.WaitGroup
}

func newBenchEchoServer(b *testing.B) *benchEchoServer {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("failed to start echo server: %v", err)
	}
	s := &benchEchoServer{listener: listener}
	go s.serve()
	return s
}

func (s *benchEchoServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer func() {
				c.Close()
				s.wg.Done()
			}()
			// Read PROXY header
			header := make([]byte, 28)
			if _, err := io.ReadFull(c, header); err != nil {
				return
			}
			// Echo
			io.Copy(c, c)
		}(conn)
	}
}

func (s *benchEchoServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *benchEchoServer) Close() {
	s.listener.Close()
	s.wg.Wait()
}

func BenchmarkRoundTripLatency(b *testing.B) {
	echoServer := newBenchEchoServer(b)
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
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		b.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	msg := make([]byte, 1024) // 1KB message

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			b.Fatal(err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMessageRelay_64B(b *testing.B) {
	benchmarkMessageRelay(b, 64)
}

func BenchmarkMessageRelay_1KB(b *testing.B) {
	benchmarkMessageRelay(b, 1024)
}

func BenchmarkMessageRelay_32KB(b *testing.B) {
	benchmarkMessageRelay(b, 32*1024)
}

func benchmarkMessageRelay(b *testing.B, size int) {
	echoServer := newBenchEchoServer(b)
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
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		b.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	msg := make([]byte, size)

	b.SetBytes(int64(size))
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			b.Fatal(err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConnectionEstablishment(b *testing.B) {
	echoServer := newBenchEchoServer(b)
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
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			b.Fatal(err)
		}
		conn.CloseNow()
	}
}

func BenchmarkConcurrentConnections_100(b *testing.B) {
	benchmarkConcurrentConnections(b, 100)
}

func BenchmarkConcurrentConnections_500(b *testing.B) {
	benchmarkConcurrentConnections(b, 500)
}

func benchmarkConcurrentConnections(b *testing.B, numConns int) {
	echoServer := newBenchEchoServer(b)
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
	ctx := context.Background()

	// Pre-establish connections
	conns := make([]*websocket.Conn, numConns)
	for i := 0; i < numConns; i++ {
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			b.Fatalf("connection %d failed: %v", i, err)
		}
		conns[i] = conn
	}

	msg := make([]byte, 64)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for _, conn := range conns {
			wg.Add(1)
			go func(c *websocket.Conn) {
				defer wg.Done()
				c.Write(ctx, websocket.MessageBinary, msg)
				c.Read(ctx)
			}(conn)
		}
		wg.Wait()
	}

	b.StopTimer()

	// Cleanup
	for _, conn := range conns {
		conn.CloseNow()
	}
}

func BenchmarkMessagesPerSecond(b *testing.B) {
	echoServer := newBenchEchoServer(b)
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
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		b.Fatalf("connection failed: %v", err)
	}
	defer conn.CloseNow()

	msg := make([]byte, 32) // Small message for throughput test

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn.Write(ctx, websocket.MessageBinary, msg)
		conn.Read(ctx)
	}
}

func BenchmarkExtractClientIP(b *testing.B) {
	handler := &ProxyHandler{
		BackendAddr: "127.0.0.1:7171",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Create a minimal http.Request for benchmarking
	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.1, 172.16.0.1")
	req.RemoteAddr = "127.0.0.1:12345"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = handler.extractClientIP(req)
	}
}

func BenchmarkParseIPPort(b *testing.B) {
	addr := "192.168.1.100:7171"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _, err := ParseIPPort(addr)
		if err != nil {
			b.Fatal(err)
		}
	}
}
