package main

import (
	"encoding/binary"
	"fmt"
	"net"
)

// PROXY protocol v2 signature (12 bytes)
var proxyV2Signature = []byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

const (
	// Version and command: v2, PROXY command
	proxyV2VersionCommand = 0x21
	// Address family and protocol: IPv4, TCP (STREAM)
	proxyV2FamilyProtocolIPv4TCP = 0x11
	// Address family and protocol: IPv6, TCP (STREAM)
	proxyV2FamilyProtocolIPv6TCP = 0x21
	// Address length for IPv4: 4 + 4 + 2 + 2 = 12 bytes
	proxyV2AddrLenIPv4 = 12
	// Address length for IPv6: 16 + 16 + 2 + 2 = 36 bytes
	proxyV2AddrLenIPv6 = 36
)

// BuildProxyV2Header constructs a PROXY protocol v2 header.
// srcIP is the client's real IP address.
// dstIP is the destination (backend) IP address.
// srcPort is the client's source port.
// dstPort is the destination (backend) port.
func BuildProxyV2Header(srcIP, dstIP net.IP, srcPort, dstPort uint16) ([]byte, error) {
	// Normalize IPs to detect IPv4 vs IPv6
	srcIPv4 := srcIP.To4()
	dstIPv4 := dstIP.To4()

	// Determine if we're using IPv4 or IPv6
	isIPv4 := srcIPv4 != nil && dstIPv4 != nil

	var header []byte
	var familyProtocol byte
	var addrLen uint16

	if isIPv4 {
		// IPv4 header: 12 (sig) + 4 (meta) + 12 (addrs) = 28 bytes
		header = make([]byte, 28)
		familyProtocol = proxyV2FamilyProtocolIPv4TCP
		addrLen = proxyV2AddrLenIPv4
	} else {
		// IPv6 header: 12 (sig) + 4 (meta) + 36 (addrs) = 52 bytes
		srcIPv6 := srcIP.To16()
		dstIPv6 := dstIP.To16()
		if srcIPv6 == nil || dstIPv6 == nil {
			return nil, fmt.Errorf("invalid IP addresses: src=%v, dst=%v", srcIP, dstIP)
		}
		header = make([]byte, 52)
		familyProtocol = proxyV2FamilyProtocolIPv6TCP
		addrLen = proxyV2AddrLenIPv6
	}

	// Copy signature (bytes 0-11)
	copy(header[0:12], proxyV2Signature)

	// Version + Command (byte 12)
	header[12] = proxyV2VersionCommand

	// Family + Protocol (byte 13)
	header[13] = familyProtocol

	// Address length (bytes 14-15, big-endian)
	binary.BigEndian.PutUint16(header[14:16], addrLen)

	if isIPv4 {
		// Source IP (bytes 16-19)
		copy(header[16:20], srcIPv4)
		// Dest IP (bytes 20-23)
		copy(header[20:24], dstIPv4)
		// Source port (bytes 24-25, big-endian)
		binary.BigEndian.PutUint16(header[24:26], srcPort)
		// Dest port (bytes 26-27, big-endian)
		binary.BigEndian.PutUint16(header[26:28], dstPort)
	} else {
		srcIPv6 := srcIP.To16()
		dstIPv6 := dstIP.To16()
		// Source IP (bytes 16-31)
		copy(header[16:32], srcIPv6)
		// Dest IP (bytes 32-47)
		copy(header[32:48], dstIPv6)
		// Source port (bytes 48-49, big-endian)
		binary.BigEndian.PutUint16(header[48:50], srcPort)
		// Dest port (bytes 50-51, big-endian)
		binary.BigEndian.PutUint16(header[50:52], dstPort)
	}

	return header, nil
}

// ParseIPPort parses an address string into IP and port.
// Handles both "ip:port" and "[ipv6]:port" formats.
func ParseIPPort(addr string) (net.IP, uint16, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to split host:port from %q: %w", addr, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Try to resolve hostname
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, 0, fmt.Errorf("failed to resolve %q: %w", host, err)
		}
		ip = ips[0]
	}

	var port uint16
	_, err = fmt.Sscanf(portStr, "%d", &port)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse port %q: %w", portStr, err)
	}

	return ip, port, nil
}
