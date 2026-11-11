package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/version"
	"github.com/liyuwei007036/polaris/internal/wire"
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
	manifest := fmt.Sprintf(`{"version":"0.40","architecture":"%s","url":"https://releases.example.invalid/polaris.tar.gz","sha256":"%s","archive":"tar.gz"}`, foreign, hash)
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
	manifest := fmt.Sprintf(`{"version":"dev","architecture":"%s","url":"https://releases.example.invalid/polaris.tar.gz","sha256":"%s","archive":"tar.gz"}`, runtime.GOARCH, hash)
	dataDir, task := signedUpgradeTask(t, manifest, hash)
	result := upgradeAgent(t.Context(), task, dataDir)
	if result.Status != "succeeded" || result.RestartAgent {
		t.Fatalf("unexpected result for already-running version: %#v", result)
	}
}

func TestUpgradeAgentSkipsDowngrade(t *testing.T) {
	prev := version.Version
	version.Version = "2026110901"
	t.Cleanup(func() { version.Version = prev })

	hash := strings.Repeat("a", 64)
	manifest := fmt.Sprintf(`{"version":"2026110604","architecture":"%s","url":"https://releases.example.invalid/polaris.tar.gz","sha256":"%s","archive":"tar.gz"}`, runtime.GOARCH, hash)
	dataDir, task := signedUpgradeTask(t, manifest, hash)
	result := upgradeAgent(t.Context(), task, dataDir)
	if result.Status != "succeeded" || result.RestartAgent {
		t.Fatalf("expected downgrade to be skipped cleanly, got: %#v", result)
	}
}

func TestTaskExecutorReevaluatesCompletedAgentUpgradeWhenStillOldVersion(t *testing.T) {
	prev := version.Version
	version.Version = "2026110604"
	t.Cleanup(func() { version.Version = prev })

	hash := strings.Repeat("a", 64)
	manifest := fmt.Sprintf(`{"version":"2026110901","architecture":"%s","url":"https://releases.example.invalid/polaris.tar.gz","sha256":"%s","archive":"tar.gz"}`, runtime.GOARCH, hash)
	dataDir, task := signedUpgradeTask(t, manifest, hash)
	task.ID = "0123456789abcdef0123456789abcdef"

	exec := newTaskExecutor(TaskOptions{DataDir: dataDir})
	// Simulate an interrupted earlier attempt that wrote completed-tasks/<id>.json
	// without RestartAgent (since RestartAgent is json:"-") while the old build is still running.
	if err := exec.saveCompleted(task, TaskResult{Status: "succeeded", Summary: "agent 已更新到版本 2026110901，正在重启"}); err != nil {
		t.Fatal(err)
	}
	called := false
	exec.execute = func(_ context.Context, _ Task, _ TaskOptions) TaskResult {
		called = true
		return TaskResult{Status: "succeeded", Summary: "restarted", RestartAgent: true}
	}

	res := exec.Handle(t.Context(), task)
	if !called || !res.RestartAgent {
		t.Fatalf("expected cached agent.upgrade on old build to re-evaluate and request restart, called=%v res=%#v", called, res)
	}
}

func TestRunSessionTriggersRestartWhenTaskCompletesAfterSessionEnds(t *testing.T) {
	masterKP, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentKP, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := wire.ListenKCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverReady := make(chan *wire.Conn, 1)
	go func() {
		raw, accErr := ln.Accept()
		if accErr != nil {
			return
		}
		c, _, hsErr := wire.AcceptXK(raw, masterKP)
		if hsErr != nil {
			return
		}
		serverReady <- c
	}()

	clientRaw, err := wire.DialKCP(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientRaw.Close()
	clientConn, err := wire.DialXK(clientRaw, agentKP, masterKP.Public)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverReady
	defer serverConn.Close()

	var restartCalls atomic.Int32
	var exitCalls atomic.Int32
	prevRestart := restartAgentFn
	prevExit := exitAfterRestartFailureFn
	prevDelay := restartFlushDelay
	restartFlushDelay = 10 * time.Millisecond
	restartAgentFn = func() error {
		restartCalls.Add(1)
		return errors.New("exec simulated error")
	}
	exitAfterRestartFailureFn = func(int) {
		exitCalls.Add(1)
	}
	t.Cleanup(func() {
		restartAgentFn = prevRestart
		exitAfterRestartFailureFn = prevExit
		restartFlushDelay = prevDelay
	})

	taskStarted := make(chan struct{})
	allowTaskFinish := make(chan struct{})
	handler := func(context.Context, Task) TaskResult {
		close(taskStarted)
		<-allowTaskFinish
		return TaskResult{Status: "succeeded", Summary: "upgraded", RestartAgent: true}
	}

	sessionCtx, cancelSession := context.WithCancel(t.Context())
	defer cancelSession()
	sessionExited := make(chan error, 1)
	go func() {
		sessionExited <- RunSession(sessionCtx, clientConn, handler, 30*time.Second, 10*time.Second, "1.14.0", t.TempDir())
	}()

	// Drain initial Status and ConnectionsPush sent at session start.
	for range 2 {
		if _, _, readErr := serverConn.ReadMessage(); readErr != nil {
			t.Fatal(readErr)
		}
	}

	taskBody, err := wire.Encode(wire.Task{ID: "abcdef0123456789abcdef0123456789", Kind: "agent.upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	if err := serverConn.WriteMessage(wire.MsgTask, taskBody); err != nil {
		t.Fatal(err)
	}
	<-taskStarted

	// Force the session to end while the upgrade task is still running,
	// reproducing a KCP timeout/disconnect mid-download.
	_ = clientConn.Close()
	select {
	case <-sessionExited:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSession did not exit after connection closed")
	}

	close(allowTaskFinish)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if restartCalls.Load() > 0 && exitCalls.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if restartCalls.Load() != 1 || exitCalls.Load() != 1 {
		t.Fatalf("expected restart and exit fallback once after session drop, got restart=%d exit=%d", restartCalls.Load(), exitCalls.Load())
	}
}

func TestFetchAgentArtifactFromMasterOverNoiseKCP(t *testing.T) {
	masterKP, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	agentKP, err := LoadOrCreateKeypair(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("polaris-relayed-binary")
	if err := tw.WriteHeader(&tar.Header{Name: "polaris_2026110901_linux_amd64/polaris", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	hashHex := hex.EncodeToString(sum[:])

	ln, err := wire.ListenKCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		raw, accErr := ln.Accept()
		if accErr != nil {
			serverErr <- accErr
			return
		}
		defer raw.Close()
		conn, peer, hsErr := wire.AcceptXK(raw, masterKP)
		if hsErr != nil {
			serverErr <- hsErr
			return
		}
		if peer != agentKP.Public {
			serverErr <- errors.New("unexpected peer key")
			return
		}
		msgType, reqBytes, rErr := conn.ReadMessage()
		if rErr != nil || msgType != wire.MsgArtifactRequest {
			serverErr <- fmt.Errorf("unexpected message type=%d err=%v", msgType, rErr)
			return
		}
		var req wire.ArtifactRequest
		if dErr := wire.Decode(reqBytes, &req); dErr != nil {
			serverErr <- dErr
			return
		}
		if req.Version != "2026110901" || req.SHA256 != hashHex {
			serverErr <- fmt.Errorf("unexpected request: %#v", req)
			return
		}
		mid := len(archive) / 2
		for _, slice := range [][]byte{archive[:mid], archive[mid:]} {
			b, _ := wire.Encode(wire.ArtifactChunk{Data: slice})
			if wErr := conn.WriteMessage(wire.MsgArtifactChunk, b); wErr != nil {
				serverErr <- wErr
				return
			}
		}
		eof, _ := wire.Encode(wire.ArtifactChunk{Done: true})
		if wErr := conn.WriteMessage(wire.MsgArtifactChunk, eof); wErr != nil {
			serverErr <- wErr
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = conn.ReadMessage()
		serverErr <- nil
	}()

	var received bytes.Buffer
	manifest := releaseManifest{Version: "2026110901", Architecture: "amd64", SHA256: hashHex}
	if err := fetchAgentArtifactFromMaster(t.Context(), dataDir, ln.Addr().String(), masterKP.Public, manifest, &received); err != nil {
		t.Fatalf("fetchAgentArtifactFromMaster failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("stub master server failed: %v", err)
	}
	if !bytes.Equal(received.Bytes(), archive) {
		t.Fatalf("received %d bytes, want %d", received.Len(), len(archive))
	}
}
