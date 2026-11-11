package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.40", "0.39", true},
		{"0.39", "0.39", false},
		{"0.39", "0.40", false},
		{"0.39.1", "0.39", true},
		{"0.39", "0.39.1", false},
		{"1.0", "0.99", true},
		{"v0.40", "v0.39", true},
		{"0.40", "dev", false},
		{"0.40", "", false},
		{"0.40", "abc1234", false},
		{"", "0.39", false},
		{"not-a-version", "0.39", false},
	}
	for _, c := range cases {
		if got := IsNewerVersion(c.latest, c.current); got != c.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestLatestPolarisRelease(t *testing.T) {
	sha := strings.Repeat("a", 64)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.40",
			"draft": false,
			"prerelease": false,
			"assets": [
				{"name": "polaris_0.40_linux_amd64.tar.gz", "browser_download_url": "https://releases.example.invalid/polaris_0.40_linux_amd64.tar.gz", "digest": "sha256:` + sha + `"},
				{"name": "polaris_0.40_linux_amd64.tar.gz.sha256", "browser_download_url": "https://releases.example.invalid/polaris_0.40_linux_amd64.tar.gz.sha256", "digest": "sha256:` + strings.Repeat("b", 64) + `"}
			]
		}`))
	}))
	defer stub.Close()
	previous := polarisReleaseAPI
	polarisReleaseAPI = stub.URL
	defer func() { polarisReleaseAPI = previous }()

	release, err := LatestPolarisRelease(t.Context(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.40" || release.Architecture != "amd64" || release.SHA256 != sha || release.Archive != "tar.gz" {
		t.Fatalf("unexpected release: %#v", release)
	}
	if _, err := LatestPolarisRelease(t.Context(), "arm64"); err == nil {
		t.Fatal("resolved a release for an architecture without a published asset")
	}
	if _, err := LatestPolarisRelease(t.Context(), "riscv64"); err == nil {
		t.Fatal("accepted an unsupported architecture")
	}
}

func TestLatestPolarisReleaseCacheAndRefresh(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server.latestPolarisReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		calls++
		return SingBoxRelease{
			Version: "0.40", Architecture: architecture, URL: "https://releases.example.invalid/polaris.tar.gz",
			SHA256: strings.Repeat("a", 64), Enabled: true, Archive: "tar.gz",
		}, nil
	}

	if _, err := server.latestPolarisRelease(t.Context(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.latestPolarisRelease(t.Context(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("release resolver called %d times; cache did not hold", calls)
	}
	if _, err := server.latestPolarisReleaseCached(t.Context(), "amd64", true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("release resolver called %d times; refresh did not bypass cache", calls)
	}
}

func TestCreateTaskAcceptsAgentUpgradeKind(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const nodeID = "agent-upgrade-node"
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "升级测试节点", []byte("agent-upgrade-public-key"), nowUnix()); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(t.Context(), Task{
		NodeID: nodeID, Kind: "agent.upgrade", IdempotencyKey: "agent-upgrade-0.40-" + strings.Repeat("a", 64),
		Payload: `{"manifest":"{}","signature":"c2ln"}`, ExpectedHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "agent.upgrade" || task.Status != "queued" {
		t.Fatalf("unexpected agent upgrade task: %#v", task)
	}
}

func TestLatestPolarisReleaseRefreshClearsAllArchitectures(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	versionTag := "2026110604"
	calls := map[string]int{}
	server.latestPolarisReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		calls[architecture]++
		return SingBoxRelease{
			Version: versionTag, Architecture: architecture,
			URL: "https://releases.example.invalid/polaris_" + architecture + ".tar.gz",
			SHA256: strings.Repeat("a", 64), Enabled: true, Archive: "tar.gz",
		}, nil
	}

	if _, err := server.latestPolarisRelease(t.Context(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.latestPolarisRelease(t.Context(), "arm64"); err != nil {
		t.Fatal(err)
	}
	versionTag = "2026110901"
	if _, err := server.latestPolarisReleaseCached(t.Context(), "amd64", true); err != nil {
		t.Fatal(err)
	}
	armRelease, err := server.latestPolarisRelease(t.Context(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if armRelease.Version != "2026110901" || calls["arm64"] != 2 {
		t.Fatalf("arm64 release stayed stale after amd64 refresh: %#v calls=%v", armRelease, calls)
	}
}

func TestPrepareAgentUpgradeTaskSupersedesStaleTask(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const nodeID = "upgrade-supersede-node"
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "升级替换节点", []byte("upgrade-supersede-pubkey"), nowUnix()); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	oldTask, err := store.CreateTask(t.Context(), Task{
		NodeID: nodeID, Kind: "agent.upgrade", IdempotencyKey: "agent-upgrade-2026110901-" + hash,
		Payload: `{"manifest":"{}","signature":"c2ln"}`, ExpectedHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTaskDispatched(t.Context(), oldTask.ID, nodeID); err != nil {
		t.Fatal(err)
	}

	// Within minAge, rapid duplicate click returns the active task.
	activeTask, active, err := store.PrepareAgentUpgradeTask(t.Context(), nodeID, hash, 15*time.Second)
	if err != nil || !active || activeTask.ID != oldTask.ID {
		t.Fatalf("expected active recent task, got active=%v task=%#v err=%v", active, activeTask, err)
	}

	// Age the task so it simulates a stuck dispatched upgrade.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE tasks SET created_at = ? WHERE id = ?`, nowUnix()-60, oldTask.ID); err != nil {
		t.Fatal(err)
	}
	_, active, err = store.PrepareAgentUpgradeTask(t.Context(), nodeID, hash, 15*time.Second)
	if err != nil || active {
		t.Fatalf("expected stale task to be superseded, got active=%v err=%v", active, err)
	}
	reloadedOld, err := store.TaskByID(t.Context(), oldTask.ID)
	if err != nil || reloadedOld.Status != "failed" {
		t.Fatalf("expected stale task marked failed, got %#v err=%v", reloadedOld, err)
	}
	newTask, err := store.CreateTask(t.Context(), Task{
		NodeID: nodeID, Kind: "agent.upgrade", IdempotencyKey: "agent-upgrade-2026110901-" + hash,
		Payload: `{"manifest":"{}","signature":"c2ln"}`, ExpectedHash: hash,
	})
	if err != nil || newTask.ID == oldTask.ID {
		t.Fatalf("expected a fresh retry task ID, got %#v err=%v", newTask, err)
	}
}

func TestMasterArtifactRelayOverNoiseKCP(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	binaryBytes := []byte("#!/bin/sh\necho 2026110901\n")
	if err := tw.WriteHeader(&tar.Header{Name: "polaris_2026110901_linux_amd64/polaris", Mode: 0o755, Size: int64(len(binaryBytes))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binaryBytes); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	digest := sha256.Sum256(archive)
	hashHex := hex.EncodeToString(digest[:])

	archiveServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer archiveServer.Close()
	prevClient := polarisArtifactHTTPClient
	polarisArtifactHTTPClient = archiveServer.Client()
	t.Cleanup(func() { polarisArtifactHTTPClient = prevClient })

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	server.latestPolarisReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		return SingBoxRelease{
			Version: "2026110901", Architecture: architecture,
			URL: archiveServer.URL + "/polaris_2026110901_linux_" + architecture + ".tar.gz",
			SHA256: hashHex, Enabled: true, Archive: "tar.gz",
		}, nil
	}

	agentKP, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, "relay-node", "中继测试节点", agentKP.Public[:], nowUnix()); err != nil {
		t.Fatal(err)
	}

	ln, err := wire.ListenKCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvCtx, cancelSrv := context.WithCancel(t.Context())
	defer cancelSrv()
	go func() {
		_ = server.ServeAgents(srvCtx, ln)
	}()

	rawConn, err := wire.DialKCP(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()
	conn, err := wire.DialXK(rawConn, agentKP, server.NoisePublicKey())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	reqBody, err := wire.Encode(wire.ArtifactRequest{
		Kind: "polaris", Version: "2026110901", Architecture: "amd64", SHA256: hashHex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(wire.MsgArtifactRequest, reqBody); err != nil {
		t.Fatal(err)
	}

	var received bytes.Buffer
	for {
		msgType, body, rErr := conn.ReadMessage()
		if rErr != nil {
			t.Fatalf("read artifact chunk: %v", rErr)
		}
		if msgType != wire.MsgArtifactChunk {
			t.Fatalf("unexpected message type %d", msgType)
		}
		var chunk wire.ArtifactChunk
		if dErr := wire.Decode(body, &chunk); dErr != nil {
			t.Fatal(dErr)
		}
		if chunk.Error != "" {
			t.Fatalf("master returned artifact error: %s", chunk.Error)
		}
		received.Write(chunk.Data)
		if chunk.Done {
			break
		}
	}
	if !bytes.Equal(received.Bytes(), archive) {
		t.Fatalf("relayed archive mismatch: got %d bytes want %d", received.Len(), len(archive))
	}
	// Verify cache file was written cleanly on the master.
	entries, err := os.ReadDir(filepath.Join(store.DataDir(), "release-cache"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 cached release file, got %v err=%v", entries, err)
	}
}

func TestUpgradeNodeAgentHTTPDispatchesAndSupersedesStaleTask(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	server.latestPolarisReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		return SingBoxRelease{
			Version: "2026110901", Architecture: architecture,
			URL: "https://releases.example.invalid/polaris_2026110901_linux_" + architecture + ".tar.gz",
			SHA256: hash, Enabled: true, Archive: "tar.gz",
		}, nil
	}

	admin, _, err := store.createOperator(ctx, "upgrade_admin", "AdminPass123456!", "admin", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.createSession(ctx, admin.ID, admin.Role, false)
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "http-upgrade-node"
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO nodes (id, name, public_key, agent_version, os, architecture, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nodeID, "HTTP升级节点", []byte("http-upgrade-pubkey"), "v2026110604", "linux", "amd64", nowUnix()); err != nil {
		t.Fatal(err)
	}

	handler := server.BrowserHandler()
	postUpgrade := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/agent/upgrade", bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec1 := postUpgrade()
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first upgrade request got %d: %s", rec1.Code, rec1.Body.String())
	}
	var task1 Task
	if err := json.NewDecoder(rec1.Body).Decode(&task1); err != nil {
		t.Fatal(err)
	}

	// Age the first task beyond the double-click window so a manual retry creates a new task.
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET created_at = ? WHERE id = ?`, nowUnix()-60, task1.ID); err != nil {
		t.Fatal(err)
	}

	rec2 := postUpgrade()
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second upgrade request got %d: %s", rec2.Code, rec2.Body.String())
	}
	var task2 Task
	if err := json.NewDecoder(rec2.Body).Decode(&task2); err != nil {
		t.Fatal(err)
	}
	if task2.ID == "" || task2.ID == task1.ID {
		t.Fatalf("expected new task ID after superseding stale task, got task1=%s task2=%s", task1.ID, task2.ID)
	}
	oldLoaded, err := store.TaskByID(ctx, task1.ID)
	if err != nil || oldLoaded.Status != "failed" {
		t.Fatalf("expected old task marked failed, got %#v err=%v", oldLoaded, err)
	}

	if err := store.UpdateNodeIdentity(ctx, nodeID, "v2026110901", "linux", "Debian 12", "amd64", "", "{}"); err != nil {
		t.Fatal(err)
	}
	rec3 := postUpgrade()
	if rec3.Code == http.StatusAccepted {
		t.Fatalf("expected already-latest node upgrade to be rejected, got 202: %s", rec3.Body.String())
	}
}
