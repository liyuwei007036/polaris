package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportedNginxConfigurationHashDetectsDrift(t *testing.T) {
	originalPath := managedNginxConfig
	managedNginxConfig = filepath.Join(t.TempDir(), "polaris.conf")
	defer func() { managedNginxConfig = originalPath }()
	dataDir := t.TempDir()
	if err := os.WriteFile(managedNginxConfig, []byte("configuration-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	desiredHash := strings.Repeat("a", 64)
	if err := saveNginxConfigurationState(dataDir, desiredHash); err != nil {
		t.Fatal(err)
	}
	if got := reportedNginxConfigurationHash(dataDir); got != desiredHash {
		t.Fatalf("reported desired hash = %q, want %q", got, desiredHash)
	}
	if err := os.WriteFile(managedNginxConfig, []byte("configuration-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := reportedNginxConfigurationHash(dataDir); got != "" {
		t.Fatalf("drifted Nginx configuration reported desired hash %q", got)
	}
}
