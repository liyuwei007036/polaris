package control

import (
	"context"
	"strings"
	"testing"
)

// The server list asks for the newest sing-box version every time it renders,
// so the lookup has to be answered from memory: GitHub only allows sixty
// anonymous calls per hour and the automatic installation needs them too.
func TestLatestSingBoxReleaseIsCachedPerArchitecture(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	server.latestSingBoxReleaseFn = func(_ context.Context, architecture string) (SingBoxRelease, error) {
		calls[architecture]++
		return SingBoxRelease{
			Version: "1.13.12", Architecture: architecture, URL: "https://releases.example.invalid/sing-box.tar.gz",
			SHA256: strings.Repeat("a", 64), Enabled: true, Archive: "tar.gz",
		}, nil
	}

	for i := 0; i < 3; i++ {
		release, err := server.latestSingBoxReleaseCached(t.Context(), "amd64")
		if err != nil {
			t.Fatal(err)
		}
		if release.Version != "1.13.12" || release.Architecture != "amd64" {
			t.Fatalf("unexpected cached release: %#v", release)
		}
	}
	if _, err := server.latestSingBoxReleaseCached(t.Context(), "arm64"); err != nil {
		t.Fatal(err)
	}
	if calls["amd64"] != 1 || calls["arm64"] != 1 {
		t.Fatalf("official release resolver calls: %#v", calls)
	}
}
