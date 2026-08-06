package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// signedUpgradeTask builds a data directory holding the release signing
// public key plus a correctly signed agent.upgrade task for the manifest.
func signedUpgradeTask(t *testing.T, manifestJSON, expectedHash string) (string, Task) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, releaseSigningPublicKeyFile), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"manifest":  manifestJSON,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(manifestJSON))),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dataDir, Task{Kind: "agent.upgrade", Payload: string(payload), ExpectedHash: expectedHash}
}

func TestUpgradeAgentRejectsForeignArchitecture(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "amd64"
	}
	hash := strings.Repeat("a", 64)
	manifest := fmt.Sprintf(`{"version":"0.40","architecture":"%s","url":"https://releases.example.invalid/sb-control.tar.gz","sha256":"%s","archive":"tar.gz"}`, foreign, hash)
	dataDir, task := signedUpgradeTask(t, manifest, hash)
	result := upgradeAgent(t.Context(), task, dataDir)
	if result.Status != "failed" || !strings.Contains(result.Summary, "architecture") {
		t.Fatalf("unexpected result for foreign architecture: %#v", result)
	}
}

func TestUpgradeAgentSkipsWhenVersionAlreadyRuns(t *testing.T) {
	// The test binary reports the default development version, so a manifest
	// carrying that version must succeed without touching any file.
	hash := strings.Repeat("a", 64)
	manifest := fmt.Sprintf(`{"version":"dev","architecture":"%s","url":"https://releases.example.invalid/sb-control.tar.gz","sha256":"%s","archive":"tar.gz"}`, runtime.GOARCH, hash)
	dataDir, task := signedUpgradeTask(t, manifest, hash)
	result := upgradeAgent(t.Context(), task, dataDir)
	if result.Status != "succeeded" || result.RestartAgent {
		t.Fatalf("unexpected result for already-running version: %#v", result)
	}
}
