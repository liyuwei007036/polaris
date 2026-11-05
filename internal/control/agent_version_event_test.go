package control

import (
	"testing"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// An agent reports its new build right after an upgrade restart. The console
// only learns about it from a live event, so a version change must be pushed
// exactly once instead of leaving the old "可升级" badge on screen.
func TestAgentVersionChangePublishesLiveEventOnce(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const nodeID = "live-version-node"
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO nodes (id, name, public_key, agent_version, created_at) VALUES (?, ?, ?, ?, ?)`,
		nodeID, "版本事件节点", []byte("live-version-public-key"), "2026010101", nowUnix()); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.NodeByID(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	events := server.liveHub.subscribe()
	defer server.liveHub.unsubscribe(events)

	status := func(version string) []byte {
		body, err := wire.Encode(wire.Status{AgentVersion: version, OS: "linux", Architecture: "amd64"})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	if !server.handleAgentMessage(t.Context(), &node, wire.MsgStatus, status("2026110405")) {
		t.Fatal("status message was not handled")
	}
	select {
	case event := <-events:
		if event.Kind != "node" || event.NodeID != nodeID || event.AgentVersion != "2026110405" {
			t.Fatalf("unexpected live event: %#v", event)
		}
	default:
		t.Fatal("version change did not publish a live event")
	}

	// The same build keeps being reported on every heartbeat; it must not
	// re-publish, otherwise every console would churn on each status frame.
	if !server.handleAgentMessage(t.Context(), &node, wire.MsgStatus, status("2026110405")) {
		t.Fatal("repeat status message was not handled")
	}
	select {
	case event := <-events:
		t.Fatalf("unchanged version published again: %#v", event)
	default:
	}

	// The in-memory session copy is updated, so a later upgrade publishes again.
	if !server.handleAgentMessage(t.Context(), &node, wire.MsgStatus, status("2026120101")) {
		t.Fatal("second upgrade status message was not handled")
	}
	select {
	case event := <-events:
		if event.AgentVersion != "2026120101" {
			t.Fatalf("unexpected live event: %#v", event)
		}
	default:
		t.Fatal("second version change did not publish a live event")
	}
}
