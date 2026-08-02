package agent

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOutboundHTTPProxyProbe(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("probe bypassed configured HTTP proxy")
	}))
	defer targetServer.Close()
	originalURL := outboundProbeURL
	outboundProbeURL = targetServer.URL
	defer func() { outboundProbeURL = originalURL }()

	address := strings.TrimPrefix(proxyServer.URL, "http://")
	host, port, ok := strings.Cut(address, ":")
	if !ok {
		t.Fatalf("unexpected proxy address %q", address)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"type": "http", "server": host, "server_port": portNumber})
	if err != nil {
		t.Fatal(err)
	}
	result := testOutbound(t.Context(), Task{Payload: string(payload)})
	if result.Status != "succeeded" || !strings.Contains(result.Summary, "HTTP 204") {
		t.Fatalf("unexpected proxy test result: %#v", result)
	}
}

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

func TestExtractSingBoxArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "sing-box.tar.gz")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	binary := "#!/bin/sh\necho sing-box\n"
	if err := tarWriter.WriteHeader(&tar.Header{Name: "sing-box-1.13.15-linux-arm64/sing-box", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(binary)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	extracted, err := extractSingBoxArchive(archivePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != strings.TrimSpace(binary) {
		t.Fatalf("unexpected extracted binary: %q", content)
	}
}
