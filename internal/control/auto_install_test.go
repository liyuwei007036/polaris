package control

import (
	"context"
	"strings"
	"testing"
)

func TestAutomaticSingBoxInstallQueuesOnlyOnce(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const nodeID = "automatic-install-node"
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "自动安装节点", []byte("automatic-install-public-key"), nowUnix()); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := 0
	server.latestSingBoxReleaseFn = func(context.Context, string) (SingBoxRelease, error) {
		resolverCalls++
		return SingBoxRelease{
			Version: "1.13.12", Architecture: "amd64", URL: "https://example.invalid/sing-box.tar.gz",
			SHA256: strings.Repeat("a", 64), Enabled: true, Archive: "tar.gz",
		}, nil
	}

	first, err := server.scheduleAutomaticSingBoxInstall(t.Context(), nodeID, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Kind != "singbox.install" || first.Status != "queued" {
		t.Fatalf("unexpected automatic installation task: %#v", first)
	}
	second, err := server.scheduleAutomaticSingBoxInstall(t.Context(), nodeID, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("duplicate automatic installation task: %#v", second)
	}
	if resolverCalls != 1 {
		t.Fatalf("official release resolver called %d times", resolverCalls)
	}
	tasks, total, err := store.ListTasks(t.Context(), nodeID, "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(tasks) != 1 || tasks[0].Kind != "singbox.install" {
		t.Fatalf("unexpected task history: total=%d tasks=%#v", total, tasks)
	}
}
