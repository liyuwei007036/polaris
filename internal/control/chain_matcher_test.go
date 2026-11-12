package control

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

func TestChainMatcherConcurrentSameDestinationPairsByTimeAndVolume(t *testing.T) {
	ctx := context.Background()
	store := createTestStore(t)

	nodeAID := createTestNode(t, store, "洛杉矶入口", "198.51.100.10")
	nodeBID := createTestNode(t, store, "台湾落地", "203.0.113.20")

	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "tw-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/vless"}},
	})
	if err != nil {
		t.Fatalf("create listener B: %v", err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relay-b", Enabled: true, OutboundID: "direct",
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create endpoint B: %v", err)
	}

	now := time.Now().UTC()
	oldStartA := now.Add(-10 * time.Minute)
	newStartA := now.Add(-200 * time.Millisecond)

	oldEntry := storedConnection{
		ID:            "a-old",
		Network:       "tcp",
		Host:          "cdn.example.com",
		Destination:   "104.16.10.10:443",
		StartedAt:     oldStartA.Format(time.RFC3339Nano),
		Upload:        120 * 1024,
		Download:      50 * 1024 * 1024,
		HasRates:      true,
		UploadRate:    1024,
		DownloadRate:  2 * 1024 * 1024,
		ChainedTarget: epB.ID,
		Outbound:      "chain-" + epB.ID,
	}
	newEntry := storedConnection{
		ID:            "a-new",
		Network:       "tcp",
		Host:          "cdn.example.com",
		Destination:   "104.16.10.10:443",
		StartedAt:     newStartA.Format(time.RFC3339Nano),
		Upload:        512,
		Download:      1024,
		HasRates:      true,
		UploadRate:    128,
		DownloadRate:  256,
		ChainedTarget: epB.ID,
		Outbound:      "chain-" + epB.ID,
	}

	// Put the NEW relay first in Node B's slice so naive array-order matching
	// would falsely let oldEntry steal relayNew.
	relayNew := storedConnection{
		ID:                  "b-new",
		Network:             "tcp",
		Destination:         "104.16.10.10:443",
		SourceIP:            "198.51.100.10",
		StartedAt:           newStartA.Add(60 * time.Millisecond).Format(time.RFC3339Nano),
		Upload:              540,
		Download:            1024,
		HasRates:            true,
		UploadRate:          130,
		DownloadRate:        250,
		Outbound:            "direct",
		OutboundName:        "直连",
		OutboundDirect:      true,
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}
	relayOld := storedConnection{
		ID:                  "b-old",
		Network:             "tcp",
		Destination:         "104.16.10.10:443",
		SourceIP:            "198.51.100.10",
		StartedAt:           oldStartA.Add(55 * time.Millisecond).Format(time.RFC3339Nano),
		Upload:              122 * 1024,
		Download:            49_800_000,
		HasRates:            true,
		UploadRate:          1050,
		DownloadRate:        1_980_000,
		Outbound:            "outbound-warp",
		OutboundName:        "TW-WARP",
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}

	hub := newConnectionsHub()
	hub.update(nodeConnectionsSnapshot{
		NodeID:      nodeAID,
		CollectedAt: now.Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns:    []storedConnection{oldEntry, newEntry},
	})
	hub.update(nodeConnectionsSnapshot{
		NodeID:      nodeBID,
		CollectedAt: now.Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns:    []storedConnection{relayNew, relayOld},
	})

	names := map[string]string{nodeAID: "洛杉矶入口", nodeBID: "台湾落地"}

	resOld := resolveChainRoute(ctx, store, hub, nil, names, nodeAID, "洛杉矶入口", oldEntry)
	if resOld.FinalOutbound != "TW-WARP" {
		t.Fatalf("oldEntry paired wrong downstream exit: got %q, want TW-WARP", resOld.FinalOutbound)
	}
	resNew := resolveChainRoute(ctx, store, hub, nil, names, nodeAID, "洛杉矶入口", newEntry)
	if resNew.FinalOutbound != "直连" || !resNew.FinalIsDirect {
		t.Fatalf("newEntry paired wrong downstream exit: got %+v, want 直连", resNew)
	}

	active := hub.activeNodeConnections()
	origForOld, okOld := findOriginEntryConn(active, hub, nodeBID, relayOld)
	if !okOld || origForOld.ID != "a-old" {
		t.Fatalf("findOriginEntryConn(relayOld) = %q (ok=%v), want a-old", origForOld.ID, okOld)
	}
	origForNew, okNew := findOriginEntryConn(active, hub, nodeBID, relayNew)
	if !okNew || origForNew.ID != "a-new" {
		t.Fatalf("findOriginEntryConn(relayNew) = %q (ok=%v), want a-new", origForNew.ID, okNew)
	}
}

func TestChainMatcherClockOffsetCalibrationAndStickyLocking(t *testing.T) {
	ctx := context.Background()
	store := createTestStore(t)

	nodeAID := createTestNode(t, store, "Entry-A", "198.51.100.1")
	nodeBID := createTestNode(t, store, "Exit-B", "203.0.113.2")

	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "b-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true}},
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "b-ep", Enabled: true, OutboundID: "direct",
	}, EndpointCredentials{Password: "pw"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	now := time.Now().UTC()
	skew := 45 * time.Second

	// Node A clock is synced; Node B clock is +45s ahead of Master.
	entryA := storedConnection{
		ID:            "entry-1",
		Network:       "udp",
		Destination:   "1.1.1.1:443",
		StartedAt:     now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano),
		Upload:        1024,
		Download:      1024,
		ChainedTarget: epB.ID,
		Outbound:      "chain-" + epB.ID,
	}

	// Decoy relay actually started 2 minutes ago on skewed Node B clock,
	// whereas real relay started right after entryA (+45s skew + 70ms RTT).
	decoyRelay := storedConnection{
		ID:                  "relay-decoy",
		Network:             "udp",
		Destination:         "1.1.1.1:443",
		SourceIP:            "198.51.100.1",
		StartedAt:           now.Add(skew - 2*time.Minute).Format(time.RFC3339Nano),
		Upload:              1024,
		Download:            1024,
		Outbound:            "outbound-wrong",
		OutboundName:        "DecoyExit",
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}
	realRelay := storedConnection{
		ID:                  "relay-real",
		Network:             "udp",
		Destination:         "1.1.1.1:443",
		SourceIP:            "198.51.100.1",
		StartedAt:           now.Add(skew - 430*time.Millisecond).Format(time.RFC3339Nano),
		Upload:              1024,
		Download:            1024,
		Outbound:            "outbound-real",
		OutboundName:        "RealExit",
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}

	hub := newConnectionsHub()
	hub.update(nodeConnectionsSnapshot{
		NodeID:      nodeAID,
		CollectedAt: now.Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns:    []storedConnection{entryA},
	})
	hub.update(nodeConnectionsSnapshot{
		NodeID:      nodeBID,
		CollectedAt: now.Add(skew).Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns:    []storedConnection{decoyRelay, realRelay},
	})

	snapB, ok := hub.node(nodeBID)
	if !ok {
		t.Fatal("expected nodeB snapshot in hub")
	}
	if math.Abs(float64(snapB.ClockOffsetMs-45000)) > 2500 {
		t.Fatalf("expected nodeB ClockOffsetMs near 45000ms, got %d", snapB.ClockOffsetMs)
	}

	names := map[string]string{nodeAID: "Entry-A", nodeBID: "Exit-B"}
	firstRes := resolveChainRoute(ctx, store, hub, nil, names, nodeAID, "Entry-A", entryA)
	if firstRes.FinalOutbound != "RealExit" {
		t.Fatalf("expected clock-calibrated match to pick RealExit, got %q", firstRes.FinalOutbound)
	}

	// Verify sticky pair was locked and survives moderate byte jitter on decoy.
	stickyTarget, stickyConnID, hasSticky := hub.stickyPairFor(nodeAID, "entry-1", 0)
	if !hasSticky || stickyTarget != nodeBID || stickyConnID != "relay-real" {
		t.Fatalf("expected sticky pair (Exit-B, relay-real), got (%q, %q, ok=%v)", stickyTarget, stickyConnID, hasSticky)
	}

	// Once entry-1 disappears from Node A's snapshot, its sticky entry is pruned.
	hub.update(nodeConnectionsSnapshot{
		NodeID:      nodeAID,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns:    []storedConnection{},
	})
	if _, _, stillPresent := hub.stickyPairFor(nodeAID, "entry-1", 0); stillPresent {
		t.Fatal("expected sticky pair to be pruned once upstream connection closed")
	}
}

func TestConvertConnectionsPerConnectionChainCacheNoCrossLeak(t *testing.T) {
	ctx := context.Background()
	store := createTestStore(t)
	srv := &Server{store: store, connHub: newConnectionsHub(), connRates: newConnectionRates(), connActivity: newNodeActivity()}

	nodeAID := createTestNode(t, store, "Entry", "198.51.100.1")
	nodeBID := createTestNode(t, store, "Landing", "203.0.113.2")

	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "b-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/vless"}},
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "b-ep", Enabled: true, OutboundID: "direct",
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	now := time.Now().UTC()
	srv.connHub.update(nodeConnectionsSnapshot{
		NodeID:      nodeBID,
		CollectedAt: now.Format(time.RFC3339),
		Connections: json.RawMessage("[]"),
		RawConns: []storedConnection{
			{
				ID:                  "b1",
				Network:             "tcp",
				Destination:         "1.1.1.1:443",
				SourceIP:            "198.51.100.1",
				StartedAt:           now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
				Upload:              10000,
				Download:            10_000_000,
				Outbound:            "outbound-special",
				OutboundName:        "SpecialLine",
				IsChainRelay:        true,
				RelayUpstreamNodeID: nodeAID,
			},
			{
				ID:                  "b2",
				Network:             "tcp",
				Destination:         "1.1.1.1:443",
				SourceIP:            "198.51.100.1",
				StartedAt:           now.Add(-100 * time.Millisecond).Format(time.RFC3339Nano),
				Upload:              200,
				Download:            500,
				Outbound:            "direct",
				OutboundName:        "直连",
				OutboundDirect:      true,
				IsChainRelay:        true,
				RelayUpstreamNodeID: nodeAID,
			},
		},
	})

	converted := srv.convertConnections(ctx, nodeAID, []wire.ConnectionInfo{
		{
			ID:          "a1",
			Network:     "tcp",
			Destination: "1.1.1.1:443",
			StartedAt:   now.Add(-5*time.Minute - 50*time.Millisecond).Format(time.RFC3339Nano),
			Upload:      10000,
			Download:    10_000_000,
			Outbound:    "chain-" + epB.ID,
			Chains:      []string{"chain-" + epB.ID},
		},
		{
			ID:          "a2",
			Network:     "tcp",
			Destination: "1.1.1.1:443",
			StartedAt:   now.Add(-150 * time.Millisecond).Format(time.RFC3339Nano),
			Upload:      200,
			Download:    500,
			Outbound:    "chain-" + epB.ID,
			Chains:      []string{"chain-" + epB.ID},
		},
	})

	if len(converted) != 2 {
		t.Fatalf("expected 2 converted connections, got %d", len(converted))
	}
	if converted[0].ResolvedOutboundName != "SpecialLine" {
		t.Fatalf("expected a1 ResolvedOutboundName = SpecialLine, got %q", converted[0].ResolvedOutboundName)
	}
	if converted[1].ResolvedOutboundName != "直连（经 Landing 落地）" {
		t.Fatalf("expected a2 ResolvedOutboundName = 直连（经 Landing 落地）, got %q", converted[1].ResolvedOutboundName)
	}
}
