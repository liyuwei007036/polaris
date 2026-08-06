package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Clash API orders chains egress-first. Reading the outbound off the last
// element reported the inbound instead, which is what made "热门代理" and the
// connection list show the entry point where the exit belonged.
func TestCollectConnectionsReadsExitFromChainHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connections":[{"id":"c1","metadata":{"network":"tcp","type":"vless","sourceIP":"198.51.100.7","sourcePort":"52011","destinationIP":"203.0.113.5","destinationPort":"443","host":"example.test"},"upload":1,"download":2,"chains":["outbound-hk","listener-abc"]}]}`)
	}))
	defer server.Close()
	original := clashAPIBase
	clashAPIBase = server.URL
	defer func() { clashAPIBase = original }()

	connections, err := CollectConnections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 {
		t.Fatalf("expected one connection, got %d", len(connections))
	}
	if connections[0].Outbound != "outbound-hk" {
		t.Fatalf("outbound = %q, want the chain head outbound-hk", connections[0].Outbound)
	}
	if connections[0].InboundTag != "listener-abc" {
		t.Fatalf("inbound tag = %q, want the chain tail listener-abc", connections[0].InboundTag)
	}
}

// Traffic shown in the console has to be traffic sing-box carried. Deriving
// it from the host interface counters made an idle node look busy, because
// those also count SSH, package updates and everything else on the machine.
func TestProxyTrafficComesFromSingBoxNotTheHostInterfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"downloadTotal":4096,"uploadTotal":1024,"connections":[]}`)
	}))
	defer server.Close()
	original := clashAPIBase
	clashAPIBase = server.URL
	defer func() { clashAPIBase = original }()

	connections, traffic, err := CollectConnectionsAndTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 0 {
		t.Fatalf("expected no connections, got %d", len(connections))
	}
	if !traffic.Available || traffic.ReceivedBytes != 4096 || traffic.SentBytes != 1024 {
		t.Fatalf("proxy traffic = %+v, want sing-box's own totals", traffic)
	}

	// With no proxied traffic the counters stand still, so the rate is zero —
	// which is the whole point: an idle node reports nothing.
	var sampler TrafficSampler
	start := time.Unix(1_700_000_000, 0)
	sampler.Sample(traffic.ReceivedBytes, traffic.SentBytes, start)
	received, sent, hasRate := sampler.Sample(traffic.ReceivedBytes, traffic.SentBytes, start.Add(5*time.Second))
	if !hasRate || received != 0 || sent != 0 {
		t.Fatalf("idle rate = %v/%v (hasRate=%v), want zero", received, sent, hasRate)
	}
}

func TestTrafficSamplerReportsRateOnlyAfterTwoSamples(t *testing.T) {
	var sampler TrafficSampler
	start := time.Unix(1_700_000_000, 0)
	if _, _, hasRate := sampler.Sample(1000, 500, start); hasRate {
		t.Fatal("the first sample cannot produce a rate")
	}
	received, sent, hasRate := sampler.Sample(3000, 1500, start.Add(2*time.Second))
	if !hasRate {
		t.Fatal("the second sample should produce a rate")
	}
	if received != 1000 || sent != 500 {
		t.Fatalf("rates = %v/%v, want 1000/500 bytes per second", received, sent)
	}
	// A counter reset must not be reported as an enormous spike.
	if _, _, hasRate := sampler.Sample(10, 5, start.Add(4*time.Second)); hasRate {
		t.Fatal("counters going backwards must not produce a rate")
	}
}

// A cached task result still has to refresh the recorded desired-state hash:
// it is what the master compares against to decide the node has converged, so
// skipping it left the master re-dispatching the same task forever.
func TestCachedNginxTaskStillRecordsDesiredState(t *testing.T) {
	root := t.TempDir()
	originalConfig := managedNginxConfig
	managedNginxConfig = filepath.Join(root, "sb-control.conf")
	defer func() { managedNginxConfig = originalConfig }()
	if err := os.WriteFile(managedNginxConfig, []byte("stream-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	executor := newTaskExecutor(TaskOptions{DataDir: dataDir})
	task := Task{
		ID: "0123456789abcdef0123456789abcdef", Kind: "nginx.apply_config",
		ExpectedHash: "1111111111111111111111111111111111111111111111111111111111111111",
	}
	// Pretend an earlier run already applied this task successfully.
	if err := executor.saveCompleted(task, TaskResult{Status: "succeeded", Summary: "applied earlier"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dataDir, "desired-state")); err != nil {
		t.Fatal(err)
	}

	result := executor.Handle(context.Background(), task)
	if result.Status != "succeeded" {
		t.Fatalf("cached task result = %#v, want the recorded success", result)
	}
	if reported := reportedNginxConfigurationHash(dataDir); reported != task.ExpectedHash {
		t.Fatalf("reported hash = %q, want %q so the master sees the node as converged", reported, task.ExpectedHash)
	}
}

func TestEnsureLogFileCreatesMissingPathOnly(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "sing-box.log")
	if err := ensureLogFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureLogFile(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("existing log content = %q, want it left untouched", content)
	}
}

// Debian and Ubuntu ship Nginx stream as a dynamic module, so their binary
// reports no --with-stream flag even when stream is installed and loaded.
// Detecting support from `nginx -V` alone rejected exactly those hosts.
func TestStreamSupportDetectedFromLoadedDynamicModule(t *testing.T) {
	loaded := "load_module modules/ngx_stream_module.so;\nhttp {\n}\n"
	if !nginxHasStreamSupport(context.Background(), loaded) {
		t.Fatal("a loaded dynamic stream module must count as stream support")
	}
	if nginxHasStreamSupport(context.Background(), "http {\n}\n") {
		t.Skip("host itself provides stream; the negative case cannot be exercised here")
	}
}

// Reapplying the same firewall configuration must replace the table rather
// than merge into it, or every publish stacks another copy of every rule.
func TestNftablesScriptReplacesTheTableRatherThanMerging(t *testing.T) {
	script := replaceableNftablesScript("table inet sb_control {\n  chain input {\n  }\n}\n")
	deleteAt := strings.Index(script, "delete table inet sb_control")
	defineAt := strings.Index(script, "table inet sb_control {")
	declareAt := strings.Index(script, "table inet sb_control\n")
	if declareAt != 0 {
		t.Fatalf("the script must open with an empty declaration so the delete is safe:\n%s", script)
	}
	if deleteAt < 0 || defineAt < deleteAt {
		t.Fatalf("the delete must precede the definition:\n%s", script)
	}
}

// Every jail's log path has to be discoverable from the compiled jail file,
// because fail2ban refuses to start a jail whose logpath is missing.
func TestLogPathPatternFindsEveryJailLogPath(t *testing.T) {
	configuration := "[sb-control-a]\nenabled = true\nlogpath = /var/log/sing-box/sing-box.log\nmaxretry = 5\n\n[sb-control-b]\nlogpath = /var/log/auth.log\n"
	matches := logPathPattern.FindAllStringSubmatch(configuration, -1)
	if len(matches) != 2 || matches[0][1] != "/var/log/sing-box/sing-box.log" || matches[1][1] != "/var/log/auth.log" {
		encoded, _ := json.Marshal(matches)
		t.Fatalf("log paths = %s, want both jail log files", encoded)
	}
}
