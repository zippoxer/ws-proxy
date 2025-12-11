package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestBuildProxyV2Header_IPv4(t *testing.T) {
	srcIP := net.ParseIP("192.168.1.100")
	dstIP := net.ParseIP("10.0.0.1")
	srcPort := uint16(54321)
	dstPort := uint16(7171)

	header, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
	if err != nil {
		t.Fatalf("BuildProxyV2Header failed: %v", err)
	}

	// Check total length (28 bytes for IPv4)
	if len(header) != 28 {
		t.Errorf("expected header length 28, got %d", len(header))
	}

	// Check signature (bytes 0-11)
	expectedSig := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	if !bytes.Equal(header[0:12], expectedSig) {
		t.Errorf("signature mismatch:\nexpected: %x\ngot:      %x", expectedSig, header[0:12])
	}

	// Check version + command (byte 12)
	if header[12] != 0x21 {
		t.Errorf("expected version+command 0x21, got 0x%02x", header[12])
	}

	// Check family + protocol (byte 13)
	if header[13] != 0x11 {
		t.Errorf("expected family+protocol 0x11, got 0x%02x", header[13])
	}

	// Check address length (bytes 14-15)
	addrLen := binary.BigEndian.Uint16(header[14:16])
	if addrLen != 12 {
		t.Errorf("expected address length 12, got %d", addrLen)
	}

	// Check source IP (bytes 16-19)
	expectedSrcIP := []byte{192, 168, 1, 100}
	if !bytes.Equal(header[16:20], expectedSrcIP) {
		t.Errorf("source IP mismatch:\nexpected: %v\ngot:      %v", expectedSrcIP, header[16:20])
	}

	// Check dest IP (bytes 20-23)
	expectedDstIP := []byte{10, 0, 0, 1}
	if !bytes.Equal(header[20:24], expectedDstIP) {
		t.Errorf("dest IP mismatch:\nexpected: %v\ngot:      %v", expectedDstIP, header[20:24])
	}

	// Check source port (bytes 24-25)
	gotSrcPort := binary.BigEndian.Uint16(header[24:26])
	if gotSrcPort != srcPort {
		t.Errorf("source port mismatch: expected %d, got %d", srcPort, gotSrcPort)
	}

	// Check dest port (bytes 26-27)
	gotDstPort := binary.BigEndian.Uint16(header[26:28])
	if gotDstPort != dstPort {
		t.Errorf("dest port mismatch: expected %d, got %d", dstPort, gotDstPort)
	}
}

func TestBuildProxyV2Header_KnownValues(t *testing.T) {
	// Test with specific known values to verify byte ordering
	srcIP := net.ParseIP("1.2.3.4")
	dstIP := net.ParseIP("5.6.7.8")
	srcPort := uint16(0x1234) // 4660
	dstPort := uint16(0x5678) // 22136

	header, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
	if err != nil {
		t.Fatalf("BuildProxyV2Header failed: %v", err)
	}

	// Verify specific byte values
	expected := []byte{
		// Signature (12 bytes)
		0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
		// Version + Command
		0x21,
		// Family + Protocol
		0x11,
		// Address length (12 in big-endian)
		0x00, 0x0C,
		// Source IP: 1.2.3.4
		0x01, 0x02, 0x03, 0x04,
		// Dest IP: 5.6.7.8
		0x05, 0x06, 0x07, 0x08,
		// Source port: 0x1234 in big-endian
		0x12, 0x34,
		// Dest port: 0x5678 in big-endian
		0x56, 0x78,
	}

	if !bytes.Equal(header, expected) {
		t.Errorf("header mismatch:\nexpected: %x\ngot:      %x", expected, header)
	}
}

func TestBuildProxyV2Header_IPv6(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	srcPort := uint16(54321)
	dstPort := uint16(7171)

	header, err := BuildProxyV2Header(srcIP, dstIP, srcPort, dstPort)
	if err != nil {
		t.Fatalf("BuildProxyV2Header failed: %v", err)
	}

	// Check total length (52 bytes for IPv6)
	if len(header) != 52 {
		t.Errorf("expected header length 52, got %d", len(header))
	}

	// Check family + protocol (byte 13) - should be 0x21 for IPv6 TCP
	if header[13] != 0x21 {
		t.Errorf("expected family+protocol 0x21 for IPv6, got 0x%02x", header[13])
	}

	// Check address length (bytes 14-15) - should be 36 for IPv6
	addrLen := binary.BigEndian.Uint16(header[14:16])
	if addrLen != 36 {
		t.Errorf("expected address length 36, got %d", addrLen)
	}
}

func TestParseIPPort(t *testing.T) {
	tests := []struct {
		addr        string
		expectedIP  string
		expectedPort uint16
		expectError bool
	}{
		{"192.168.1.1:8080", "192.168.1.1", 8080, false},
		{"10.0.0.1:7171", "10.0.0.1", 7171, false},
		{"127.0.0.1:80", "127.0.0.1", 80, false},
		{"[::1]:8080", "::1", 8080, false},
		{"invalid", "", 0, true},
		{":8080", "", 0, true}, // empty host
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			ip, port, err := ParseIPPort(tt.addr)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error for %q, got none", tt.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.addr, err)
			}
			if ip.String() != tt.expectedIP {
				t.Errorf("IP mismatch for %q: expected %s, got %s", tt.addr, tt.expectedIP, ip.String())
			}
			if port != tt.expectedPort {
				t.Errorf("port mismatch for %q: expected %d, got %d", tt.addr, tt.expectedPort, port)
			}
		})
	}
}

func TestProxyV2Signature(t *testing.T) {
	// Verify the signature matches the PROXY protocol v2 spec
	expected := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	if !bytes.Equal(proxyV2Signature, expected) {
		t.Errorf("signature constant mismatch:\nexpected: %x\ngot:      %x", expected, proxyV2Signature)
	}
}
