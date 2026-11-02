package agent

import (
	"context"
	"encoding/json"
	"net"
	"testing"
)

func TestParseLinuxPingOutput(t *testing.T) {
	sampleLinux := `PING 1.1.1.1 (1.1.1.1) 56(84) bytes of data.
64 bytes from 1.1.1.1: icmp_seq=1 ttl=59 time=12.4 ms
64 bytes from 1.1.1.1: icmp_seq=2 ttl=59 time=13.1 ms
64 bytes from 1.1.1.1: icmp_seq=3 ttl=59 time=11.9 ms
64 bytes from 1.1.1.1: icmp_seq=4 ttl=59 time=14.2 ms
64 bytes from 1.1.1.1: icmp_seq=5 ttl=59 time=12.8 ms

--- 1.1.1.1 ping statistics ---
5 packets transmitted, 5 received, 0% packet loss, time 803ms
rtt min/avg/max/mdev = 11.900/12.880/14.200/0.780 ms`

	stats, ok := parsePingOutput(sampleLinux, 5)
	if !ok {
		t.Fatalf("expected parsePingOutput to succeed")
	}
	if stats.Sent != 5 || stats.Received != 5 {
		t.Errorf("expected sent=5, received=5, got sent=%d, received=%d", stats.Sent, stats.Received)
	}
	if stats.LossPercent != 0.0 {
		t.Errorf("expected 0 loss, got %f", stats.LossPercent)
	}
	if stats.MinLatencyMs != 11.9 || stats.AvgLatencyMs != 12.88 || stats.MaxLatencyMs != 14.2 {
		t.Errorf("unexpected RTT stats: min=%f, avg=%f, max=%f", stats.MinLatencyMs, stats.AvgLatencyMs, stats.MaxLatencyMs)
	}
}

func TestParseWindowsPingOutput(t *testing.T) {
	sampleWin := `Pinging 1.1.1.1 with 32 bytes of data:
Reply from 1.1.1.1: bytes=32 time=15ms TTL=57
Reply from 1.1.1.1: bytes=32 time=16ms TTL=57

Ping statistics for 1.1.1.1:
    Packets: Sent = 4, Received = 4, Lost = 0 (0% loss),
Approximate round trip times in milli-seconds:
    Minimum = 15ms, Maximum = 16ms, Average = 15ms`

	stats, ok := parsePingOutput(sampleWin, 4)
	if !ok {
		t.Fatalf("expected parsePingOutput to succeed on Windows output")
	}
	if stats.Sent != 4 || stats.Received != 4 {
		t.Errorf("expected sent=4, received=4, got sent=%d, received=%d", stats.Sent, stats.Received)
	}
	if stats.LossPercent != 0.0 {
		t.Errorf("expected 0 loss, got %f", stats.LossPercent)
	}
	if stats.AvgLatencyMs != 15.0 {
		t.Errorf("expected avg 15ms, got %f", stats.AvgLatencyMs)
	}
}

func TestExecuteTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			_ = conn.Close()
		}
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	stats := executeTCPProbe(context.Background(), "127.0.0.1", uint16(tcpAddr.Port), 4, 3)
	if stats.Sent != 3 {
		t.Errorf("expected sent=3, got %d", stats.Sent)
	}
	if stats.Received != 3 {
		t.Errorf("expected received=3, got %d", stats.Received)
	}
	if stats.LossPercent != 0.0 {
		t.Errorf("expected 0 loss, got %f", stats.LossPercent)
	}
}

func TestRunNetworkProbeTask(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			_ = conn.Close()
		}
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	payloadJSON, _ := json.Marshal(NetworkProbeRequest{
		Host:   "127.0.0.1",
		Port:   uint16(tcpAddr.Port),
		Count:  2,
		Method: "tcp",
	})

	task := Task{
		ID:      "test-probe-1",
		Kind:    "network.probe",
		Payload: string(payloadJSON),
	}

	res := runNetworkProbe(context.Background(), task)
	if res.Status != "succeeded" {
		t.Fatalf("runNetworkProbe failed: %s, summary: %s", res.Status, res.Summary)
	}

	var resp NetworkProbeResponse
	if err := json.Unmarshal([]byte(res.Data), &resp); err != nil {
		t.Fatalf("failed to unmarshal probe data: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected resp.OK = true")
	}
	if resp.Sent != 2 || resp.Received != 2 {
		t.Errorf("expected sent=2, received=2, got sent=%d, received=%d", resp.Sent, resp.Received)
	}
}
