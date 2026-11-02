package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRunSpeedtest(t *testing.T) {
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
