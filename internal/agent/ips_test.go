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
