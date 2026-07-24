package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyReleaseTaskRejectsTampering(t *testing.T) {
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
	manifest := `{"version":"1.13.12","architecture":"amd64","url":"https://releases.example.invalid/sing-box","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	payload, err := json.Marshal(map[string]string{
		"manifest":  manifest,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(manifest))),
	})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{Payload: string(payload), ExpectedHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := VerifyReleaseTask(dataDir, task); err != nil {
		t.Fatalf("verify signed release task: %v", err)
	}
	task.ExpectedHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := VerifyReleaseTask(dataDir, task); err == nil {
		t.Fatal("accepted task with a different expected hash")
	}
	var altered map[string]string
	if err := json.Unmarshal(payload, &altered); err != nil {
		t.Fatal(err)
	}
	altered["manifest"] = `{"version":"1.13.12","architecture":"amd64","url":"https://attacker.invalid/sing-box","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	payload, err = json.Marshal(altered)
	if err != nil {
		t.Fatal(err)
	}
	task = Task{Payload: string(payload), ExpectedHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := VerifyReleaseTask(dataDir, task); err == nil {
		t.Fatal("accepted task with a modified manifest")
	}
}
