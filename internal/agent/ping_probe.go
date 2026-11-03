package agent

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// PingProbeRunner manages background periodic TCP latency probes.
type PingProbeRunner struct {
	mu      sync.RWMutex
	targets []pingTarget
	results []wire.PingResult
	cancel  context.CancelFunc
}

type pingTarget struct {
	ID   string
	Name string
	Addr string
}

var globalPingRunner = func() *PingProbeRunner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &PingProbeRunner{
		targets: defaultPingTargetList(),
		cancel:  cancel,
	}
	go r.loop(ctx)
	return r
}()

// GetLatestPingResults returns a snapshot of the most recent probe results.
func GetLatestPingResults() []wire.PingResult {
	return globalPingRunner.getResults()
}

// AddPingTarget dynamically appends a custom probe endpoint (e.g. master address or client target).
func AddPingTarget(target string) {
	globalPingRunner.addTarget(pingTarget{ID: target, Name: target, Addr: target})
}

func defaultPingTargetList() []pingTarget {
	return []pingTarget{}
}

func ConfigurePingTargets(targets []PingTargetConfig) {
	list := make([]pingTarget, 0, len(targets))
	for _, target := range targets {
		if target.ID != "" && target.Addr != "" {
			list = append(list, pingTarget{ID: target.ID, Name: target.Name, Addr: target.Addr})
		}
	}
	globalPingRunner.configure(list)
}

type PingTargetConfig struct{ ID, Name, Addr string }

func (r *PingProbeRunner) configure(targets []pingTarget) {
	r.mu.Lock()
	r.targets = targets
	r.mu.Unlock()
	r.runProbe()
}

func (r *PingProbeRunner) addTarget(target pingTarget) {
	if target.Addr == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.targets {
		if t.Addr == target.Addr {
			return
		}
	}
	r.targets = append(r.targets, target)
}

func (r *PingProbeRunner) getResults() []wire.PingResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]wire.PingResult, len(r.results))
	copy(res, r.results)
	return res
}

func (r *PingProbeRunner) loop(ctx context.Context) {
	// First probe immediately
	r.runProbe()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runProbe()
		}
	}
}

func (r *PingProbeRunner) runProbe() {
	r.mu.RLock()
	targets := make([]pingTarget, len(r.targets))
	copy(targets, r.targets)
	r.mu.RUnlock()

	var wg sync.WaitGroup
	results := make([]wire.PingResult, len(targets))

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target pingTarget) {
			defer wg.Done()
			lat := measureTCPPing(target.Addr, 3*time.Second)
			results[idx] = wire.PingResult{
				ProbeID:   target.ID,
				ProbeName: target.Name,
				Target:    target.Addr,
				LatencyMs: lat,
				Timestamp: time.Now().Unix(),
			}
		}(i, t)
	}
	wg.Wait()

	r.mu.Lock()
	r.results = results
	r.mu.Unlock()
}

// measureTCPPing measures the TCP handshake connect time to a host:port.
// If no port is specified, port 53 (DNS TCP standard) is assumed.
// Returns latency in milliseconds, or -1 if the connection timed out or failed.
func measureTCPPing(target string, timeout time.Duration) int {
	addr := strings.TrimSpace(target)
	if addr == "" {
		return -1
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "53")
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return -1
	}
	_ = conn.Close()
	return int(time.Since(start).Milliseconds())
}
