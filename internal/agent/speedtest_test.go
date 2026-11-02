package agent

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
)

func TestRunSpeedtest(t *testing.T) {
	// If running under root on Linux during unit test, mock URL to avoid executing
	// live 16-thread benchmark against external services unless explicitly requested.
	if os.Getenv("POLARIS_LIVE_SPEEDTEST") == "" {
		t.Setenv("POLARIS_TCPQUALITY_URL", "https://tcpquality.ibsgss.uk/r/mock-test-report")
	}

	ctx := context.Background()
	task := Task{
		ID:   "task-speedtest-1",
		Kind: "speedtest.run",
	}

	result := runSpeedtest(ctx, task)
	if result.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %s: %s", result.Status, result.Summary)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}

	reportURL, ok := data["report_url"].(string)
	if !ok || reportURL == "" {
		t.Fatalf("expected report_url string, got %v", data["report_url"])
	}
}

func TestRunSpeedtest_NonRootSimulation(t *testing.T) {
	// Test the fallback logic when POLARIS_TCPQUALITY_URL is not set in non-root or non-Linux environment
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		t.Skip("skipping non-root simulation test when running as root")
	}

	ctx := context.Background()
	task := Task{
		ID:   "task-speedtest-nonroot",
		Kind: "speedtest.run",
	}

	result := runSpeedtest(ctx, task)
	if result.Status != "succeeded" {
		t.Fatalf("expected succeeded in non-root simulation, got %s: %s", result.Status, result.Summary)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if data["report_url"] != "https://tcpquality.ibsgss.uk/r/mock-dev-report" {
		t.Fatalf("unexpected mock report_url: %v", data["report_url"])
	}
}

func TestTcpQualityRegex(t *testing.T) {
	sampleOutputs := []struct {
		text string
		want string
	}{
		{
			text: "  报告链接：https://tcpquality.ibsgss.uk/r/dqS4ipobeO\n今日TCP脚本使用次数",
			want: "https://tcpquality.ibsgss.uk/r/dqS4ipobeO",
		},
		{
			text: `{"id":"xyz123","url":"https://tcpquality.ibsgss.uk/r/xyz123"}`,
			want: "https://tcpquality.ibsgss.uk/r/xyz123",
		},
		{
			text: "  报告链接：\x1b[4mhttps://tcpquality.ibsgss.uk/r/abc_123-def\x1b[0m",
			want: "https://tcpquality.ibsgss.uk/r/abc_123-def",
		},
	}

	for _, s := range sampleOutputs {
		matches := tcpQualityURLRegex.FindAllString(s.text, -1)
		if len(matches) == 0 {
			t.Fatalf("failed to match URL in: %s", s.text)
		}
		got := matches[len(matches)-1]
		if got != s.want {
			t.Errorf("got %q, want %q", got, s.want)
		}
	}
}
