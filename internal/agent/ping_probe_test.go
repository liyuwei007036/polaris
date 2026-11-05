package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPingTargetsPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ping-probe-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	targets := []PingTargetConfig{
		{ID: "1", Name: "Test Probe 1", Addr: "127.0.0.1:53"},
		{ID: "2", Name: "Test Probe 2", Addr: "1.1.1.1:53"},
	}

	// 1. Configure targets with dataDir
	ConfigurePingTargets(targets, tempDir)

	// Verify file was written to disk
	targetFile := filepath.Join(tempDir, pingTargetsFileName)
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("expected ping_targets.json to be created, got error: %v", err)
	}

	var saved []pingTarget
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse saved ping_targets.json: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved targets, got %d", len(saved))
	}
	if saved[0].Name != "Test Probe 1" || saved[1].Addr != "1.1.1.1:53" {
		t.Fatalf("unexpected saved targets: %+v", saved)
	}

	// 2. Test cold start loading from dataDir in a fresh runner
	freshRunner := &PingProbeRunner{}
	freshRunner.initDataDir(tempDir)

	freshRunner.mu.RLock()
	loadedTargets := freshRunner.targets
	freshRunner.mu.RUnlock()

	if len(loadedTargets) != 2 {
		t.Fatalf("expected 2 targets loaded on cold start, got %d", len(loadedTargets))
	}
	if loadedTargets[0].ID != "1" || loadedTargets[1].Name != "Test Probe 2" {
		t.Fatalf("mismatched loaded targets: %+v", loadedTargets)
	}
}
