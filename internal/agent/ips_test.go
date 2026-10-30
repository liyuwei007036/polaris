package agent

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestEnumerateHostCandidateIPs(t *testing.T) {
	ips := EnumerateHostCandidateIPs()
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Errorf("expected valid IP, got %q", ipStr)
		}
		if ip.IsLoopback() {
			t.Errorf("expected no loopback IP, got %q", ipStr)
		}
		if ip.IsLinkLocalUnicast() {
			t.Errorf("expected no link-local IP, got %q", ipStr)
		}
	}
}

func TestIsPublicIPv6(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"::1", false},                             // loopback
		{"::", false},                              // unspecified
		{"fe80::1", false},                         // link-local
		{"fc00::1", false},                         // ULA
		{"fd00::1", false},                         // ULA
		{"ff02::1", false},                         // multicast
		{"192.168.1.1", false},                     // IPv4
		{"2400:3200::1", true},                     // Global IPv6
		{"2606:4700:4700::1111", true},             // Global IPv6
		{"2001:4860:4860::8888", true},             // Global IPv6
		{"2a01:4f8:c010:1234::1", true},            // Global IPv6
	}

	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %s", tc.ip)
		}
		if got := isPublicIPv6(ip); got != tc.expected {
			t.Errorf("isPublicIPv6(%s) = %v, expected %v", tc.ip, got, tc.expected)
		}
	}
}

func TestCanAccessInternetWithMockTarget(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("skipping local listener test:", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	SetProbeTargetsForTest([]string{ln.Addr().String()}, nil)
	defer SetProbeTargetsForTest(nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 127.0.0.1 dialing 127.0.0.1 should succeed
	if !CanAccessInternet(ctx, "127.0.0.1") {
		t.Error("expected 127.0.0.1 to successfully dial local listener")
	}

	// Bogus IP dialing should fail
	if CanAccessInternet(ctx, "192.0.2.1") {
		t.Error("expected unreachable IP to fail internet access check")
	}
}
