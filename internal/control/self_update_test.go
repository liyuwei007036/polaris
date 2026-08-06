package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestLatestSBControlRelease(t *testing.T) {
	sha := strings.Repeat("a", 64)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.40",
			"draft": false,
			"prerelease": false,
			"assets": [
				{"name": "sb-control_0.40_linux_amd64.tar.gz", "browser_download_url": "https://releases.example.invalid/sb-control_0.40_linux_amd64.tar.gz", "digest": "sha256:` + sha + `"},
				{"name": "sb-control_0.40_linux_amd64.tar.gz.sha256", "browser_download_url": "https://releases.example.invalid/sb-control_0.40_linux_amd64.tar.gz.sha256", "digest": "sha256:` + strings.Repeat("b", 64) + `"}
			]
		}`))
	}))
	defer stub.Close()
	previous := sbControlReleaseAPI
	sbControlReleaseAPI = stub.URL
	defer func() { sbControlReleaseAPI = previous }()

	release, err := LatestSBControlRelease(t.Context(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.40" || release.Architecture != "amd64" || release.SHA256 != sha || release.Archive != "tar.gz" {
		t.Fatalf("unexpected release: %#v", release)
	}
	if _, err := LatestSBControlRelease(t.Context(), "arm64"); err == nil {
		t.Fatal("resolved a release for an architecture without a published asset")
	}
	if _, err := LatestSBControlRelease(t.Context(), "riscv64"); err == nil {
		t.Fatal("accepted an unsupported architecture")
	}
}

func TestLatestSBControlReleaseCacheAndRefresh(t *testing.T) {
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
	server.latestSBControlReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		calls++
		return SingBoxRelease{
			Version: "0.40", Architecture: architecture, URL: "https://releases.example.invalid/sb-control.tar.gz",
			SHA256: strings.Repeat("a", 64), Enabled: true, Archive: "tar.gz",
		}, nil
	}

	if _, err := server.latestSBControlRelease(t.Context(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.latestSBControlRelease(t.Context(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("release resolver called %d times; cache did not hold", calls)
	}
	if _, err := server.latestSBControlReleaseCached(t.Context(), "amd64", true); err != nil {
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
