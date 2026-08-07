package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func buildArchive(t *testing.T, binaryName, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "polaris_0.40_linux_amd64/" + binaryName, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func serveArchive(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	previous := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = previous })
	return server
}

func TestApplyReplacesBinaryAndBacksUpPrevious(t *testing.T) {
	archive := buildArchive(t, "polaris", "new-binary-content")
	server := serveArchive(t, archive)
	digest := sha256.Sum256(archive)

	directory := t.TempDir()
	executable := filepath.Join(directory, "polaris")
	if err := os.WriteFile(executable, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	verified := false
	manifest := Manifest{Version: "0.40", URL: server.URL, SHA256: hex.EncodeToString(digest[:]), Archive: "tar.gz"}
	err := Apply(t.Context(), manifest, executable, func(_ context.Context, binaryPath string) error {
		verified = true
		content, err := os.ReadFile(binaryPath)
		if err != nil {
			return err
		}
		if string(content) != "new-binary-content" {
			return errors.New("unexpected installed content")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("verify callback did not run")
	}
	installed, err := os.ReadFile(executable)
	if err != nil || string(installed) != "new-binary-content" {
		t.Fatalf("installed binary content %q err=%v", installed, err)
	}
	backup, err := os.ReadFile(executable + ".polaris.last-good")
	if err != nil || string(backup) != "old-binary-content" {
		t.Fatalf("backup content %q err=%v", backup, err)
	}
}

func TestApplyRollsBackWhenVerificationFails(t *testing.T) {
	archive := buildArchive(t, "polaris", "broken-binary")
	server := serveArchive(t, archive)
	digest := sha256.Sum256(archive)

	directory := t.TempDir()
	executable := filepath.Join(directory, "polaris")
	if err := os.WriteFile(executable, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{Version: "0.40", URL: server.URL, SHA256: hex.EncodeToString(digest[:]), Archive: "tar.gz"}
	err := Apply(t.Context(), manifest, executable, func(context.Context, string) error {
		return errors.New("verification rejected the binary")
	})
	if err == nil {
		t.Fatal("apply succeeded despite failed verification")
	}
	restored, readErr := os.ReadFile(executable)
	if readErr != nil || string(restored) != "old-binary-content" {
		t.Fatalf("binary not rolled back: %q err=%v", restored, readErr)
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	archive := buildArchive(t, "polaris", "any-content")
	server := serveArchive(t, archive)

	directory := t.TempDir()
	executable := filepath.Join(directory, "polaris")
	if err := os.WriteFile(executable, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{Version: "0.40", URL: server.URL, SHA256: hex.EncodeToString(bytes.Repeat([]byte{0xaa}, sha256.Size)), Archive: "tar.gz"}
	err := Apply(t.Context(), manifest, executable, func(context.Context, string) error { return nil })
	if err == nil {
		t.Fatal("apply accepted a mismatched checksum")
	}
	current, readErr := os.ReadFile(executable)
	if readErr != nil || string(current) != "old-binary-content" {
		t.Fatalf("binary changed despite checksum failure: %q err=%v", current, readErr)
	}
}

func TestApplyRejectsArchiveWithoutBinary(t *testing.T) {
	archive := buildArchive(t, "README.md", "not a binary")
	server := serveArchive(t, archive)
	digest := sha256.Sum256(archive)

	directory := t.TempDir()
	executable := filepath.Join(directory, "polaris")
	if err := os.WriteFile(executable, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{Version: "0.40", URL: server.URL, SHA256: hex.EncodeToString(digest[:]), Archive: "tar.gz"}
	if err := Apply(t.Context(), manifest, executable, func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("apply accepted an archive without the polaris binary")
	}
}
