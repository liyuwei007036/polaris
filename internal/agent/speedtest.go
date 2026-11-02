package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type SpeedtestResult struct {
	ReportURL string `json:"report_url"`
}

var (
	tcpQualityURLRegex = regexp.MustCompile(`https?://[^\s"'<>\x1b]+/r/[a-zA-Z0-9_-]+`)
)

func runSpeedtest(ctx context.Context, task Task) TaskResult {
	if testURL := os.Getenv("POLARIS_TCPQUALITY_URL"); testURL != "" {
		resJSON, _ := json.Marshal(map[string]any{
			"report_url": testURL,
		})
		return TaskResult{
			Status:  "succeeded",
			Data:    string(resJSON),
			Summary: fmt.Sprintf("TcpQuality 检测完成: %s", testURL),
		}
	}

	// For non-Linux OS (e.g. Windows/macOS local dev or test environments)
	if runtime.GOOS != "linux" {
		resJSON, _ := json.Marshal(map[string]any{
			"report_url": "https://tcpquality.ibsgss.uk/r/mock-dev-report",
		})
		return TaskResult{
			Status:  "succeeded",
			Data:    string(resJSON),
			Summary: "TcpQuality 检测完成 (本地开发模拟)",
		}
	}

	// On Linux servers, execute TcpQuality via bash script
	timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var payload struct {
		Args []string `json:"args,omitempty"`
	}
	_ = json.Unmarshal([]byte(task.Payload), &payload)

	cliArgs := payload.Args
	if len(cliArgs) == 0 {
		// By default test both IPv4 and IPv6 (dual-stack) with 4 packets and 16 parallel threads.
		// Passing non-debug args prevents the rootfs wrapper from waiting on interactive stdin.
		cliArgs = []string{"-c", "4", "-p", "16"}
	}

	escapedArgs := make([]string, len(cliArgs))
	for i, arg := range cliArgs {
		escapedArgs[i] = fmt.Sprintf("%q", arg)
	}
	argsStr := strings.Join(escapedArgs, " ")

	script := fmt.Sprintf(`
set -e
export TERM=xterm
export DEBIAN_FRONTEND=noninteractive

tmp=$(mktemp /tmp/tcpquality_run.XXXXXX.sh 2>/dev/null || mktemp)
cleanup() {
    rm -rf "$tmp"
}
trap cleanup EXIT

if curl -fsSL --retry 3 --connect-timeout 15 https://tcpquality.ibsgss.uk/run -o "$tmp" || \
   curl -fsSL --retry 3 --connect-timeout 15 https://raw.githubusercontent.com/ibsgss/TcpQuality/main/runTcpQuality.sh -o "$tmp"; then
    chmod 0755 "$tmp"
    bash "$tmp" %s
    exit $?
fi

echo "Failed to download TcpQuality script" >&2
exit 1
`, argsStr)
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "TERM=xterm", "DEBIAN_FRONTEND=noninteractive")

	outBytes, err := cmd.CombinedOutput()
	output := string(outBytes)

	// Extract report URL from output
	reportURL := ""
	matches := tcpQualityURLRegex.FindAllString(output, -1)
	if len(matches) > 0 {
		reportURL = matches[len(matches)-1]
	}

	if reportURL == "" {
		if err != nil {
			tail := output
			if len(tail) > 400 {
				tail = tail[len(tail)-400:]
			}
			return TaskResult{
				Status:  "failed",
				Summary: fmt.Sprintf("TcpQuality 运行失败: %v, 输出: %s", err, strings.TrimSpace(tail)),
			}
		}
		return TaskResult{
			Status:  "failed",
			Summary: "TcpQuality 执行完成，但未在输出中解析到报告链接",
		}
	}

	resJSON, _ := json.Marshal(map[string]any{
		"report_url": reportURL,
	})

	return TaskResult{
		Status:  "succeeded",
		Data:    string(resJSON),
		Summary: fmt.Sprintf("TcpQuality 检测完成: %s", reportURL),
	}
}
