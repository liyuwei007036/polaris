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

func TestNginxHTTPListenerDetection(t *testing.T) {
	for name, test := range map[string]struct {
		configuration string
		want          bool
	}{
		"default IPv4": {"server {\n listen 80 default_server;\n}", true},
		"default IPv6": {"server {\n listen [::]:80;\n}", true},
		"managed TCP":  {"server {\n listen 0.0.0.0:443;\n ssl_preread on;\n}", false},
		"comment":      {"# listen 80;", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := nginxHasHTTPListener(test.configuration); got != test.want {
				t.Fatalf("nginxHasHTTPListener() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRaiseNginxWorkerConnections(t *testing.T) {
	configuration := []byte("worker_processes auto;\nevents {\n\tworker_connections 768;\n}\n")
	updated, changed := raiseNginxWorkerConnections(configuration)
	if !changed || !strings.Contains(string(updated), "worker_connections 4096;") {
		t.Fatalf("worker connection limit was not raised:\n%s", updated)
	}
	updated, changed = raiseNginxWorkerConnections(updated)
	if changed || !strings.Contains(string(updated), "worker_connections 4096;") {
		t.Fatalf("adequate worker connection limit was changed:\n%s", updated)
	}
}

func TestRaiseNginxWorkerOpenFiles(t *testing.T) {
	configuration := []byte("worker_processes auto;\nevents {\n\tworker_connections 4096;\n}\n")
	updated, changed := raiseNginxWorkerOpenFiles(configuration)
	if !changed || !strings.HasPrefix(string(updated), "worker_rlimit_nofile 65535;\n") {
		t.Fatalf("worker file limit was not added:\n%s", updated)
	}
	updated, changed = raiseNginxWorkerOpenFiles(updated)
	if changed || strings.Count(string(updated), "worker_rlimit_nofile") != 1 {
		t.Fatalf("adequate worker file limit was changed:\n%s", updated)
	}

	configuration = []byte("worker_rlimit_nofile 1024;\nworker_processes auto;\n")
	updated, changed = raiseNginxWorkerOpenFiles(configuration)
	if !changed || !strings.Contains(string(updated), "worker_rlimit_nofile 65535;") {
		t.Fatalf("worker file limit was not raised:\n%s", updated)
	}
}

func TestInitialSingBoxConfigIsValidJSON(t *testing.T) {
	var configuration map[string]any
	if err := json.Unmarshal([]byte(initialSingBoxConfig), &configuration); err != nil {
		t.Fatalf("initial sing-box configuration is invalid JSON: %v", err)
	}
	if _, ok := configuration["outbounds"]; !ok {
		t.Fatal("initial sing-box configuration has no outbound")
	}
	experimental, ok := configuration["experimental"].(map[string]any)
	if !ok {
		t.Fatal("initial sing-box configuration has no local connection API")
	}
	clashAPI, ok := experimental["clash_api"].(map[string]any)
	if !ok || clashAPI["external_controller"] != "127.0.0.1:9090" {
		t.Fatalf("unexpected local connection API configuration: %#v", experimental)
	}
}

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
