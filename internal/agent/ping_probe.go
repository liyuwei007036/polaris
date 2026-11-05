package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

const pingTargetsFileName = "ping_targets.json"

// PingProbeRunner manages background periodic TCP latency probes.
type PingProbeRunner struct {
	mu      sync.RWMutex
	targets []pingTarget
	results []wire.PingResult
	cancel  context.CancelFunc
	dataDir string
}

type pingTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Addr string `json:"addr"`
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

// InitPingRunner initializes the global runner with the agent data directory and loads cached targets from disk.
func InitPingRunner(dataDir string) {
	globalPingRunner.initDataDir(dataDir)
}

func (r *PingProbeRunner) initDataDir(dataDir string) {
	if dataDir == "" {
		return
	}
	r.mu.Lock()
	r.dataDir = dataDir
	filePath := filepath.Join(dataDir, pingTargetsFileName)
	data, err := os.ReadFile(filePath)
	if err == nil && len(data) > 0 {
		var list []pingTarget
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			r.targets = list
		}
	}
	r.mu.Unlock()
	r.runProbe()
}

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

func ConfigurePingTargets(targets []PingTargetConfig, dataDir ...string) {
	list := make([]pingTarget, 0, len(targets))
	for _, target := range targets {
		if target.ID != "" && target.Addr != "" {
			list = append(list, pingTarget{ID: target.ID, Name: target.Name, Addr: target.Addr})
		}
	}
	if len(dataDir) > 0 && dataDir[0] != "" {
		globalPingRunner.mu.Lock()
		globalPingRunner.dataDir = dataDir[0]
		globalPingRunner.mu.Unlock()
	}
	globalPingRunner.configure(list)
}

type PingTargetConfig struct{ ID, Name, Addr string }

func (r *PingProbeRunner) configure(targets []pingTarget) {
	r.mu.Lock()
	r.targets = targets
	dataDir := r.dataDir
	r.mu.Unlock()

	if dataDir != "" {
		if data, err := json.MarshalIndent(targets, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dataDir, pingTargetsFileName), data, 0o644)
		}
	}
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
