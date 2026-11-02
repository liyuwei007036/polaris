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

	// For non-Linux OS or non-root environments (e.g. Windows/macOS, CI runners, or local dev without root)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		resJSON, _ := json.Marshal(map[string]any{
			"report_url": "https://tcpquality.ibsgss.uk/r/mock-dev-report",
		})
		return TaskResult{
			Status:  "succeeded",
			Data:    string(resJSON),
			Summary: "TcpQuality 检测完成 (本地开发/非root环境模拟)",
		}
	}

	// On Linux servers, execute TcpQuality via bash script
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute+45*time.Second)
	defer cancel()

	var payload struct {
		Args []string `json:"args,omitempty"`
	}
	_ = json.Unmarshal([]byte(task.Payload), &payload)

	hasNoRootfs := false
	hasScopeFlag := false
	for _, arg := range payload.Args {
		if arg == "--no-rootfs" {
			hasNoRootfs = true
		}
		if arg == "--all" || arg == "-v4" || arg == "-v6" || arg == "--intl" || arg == "--route" || arg == "--speedtest" {
			hasScopeFlag = true
		}
	}
	cliArgs := payload.Args
	if !hasNoRootfs {
		cliArgs = append([]string{"--no-rootfs"}, cliArgs...)
	}
	if !hasScopeFlag {
		// Include --all so TcpQuality performs the complete multi-vector detection:
		// IPv4 回程, IPv6 回程 (自适应检测), IPv4 大包回程, 教育网回程 (CERNET), 国际互联, 单线程测速.
		cliArgs = append(cliArgs, "--all")
	}
	if len(payload.Args) == 0 {
		// Enforce safe options:
		// 1. --no-rootfs: prevents downloading 500MB rootfs and unmounting host /dev
		// 2. --all: comprehensive multi-tab detection
		// 3. -c 4: fast pulse packet count (4 packets instead of 25)
		// 4. -p 16: 16 parallel threads for high efficiency
		cliArgs = []string{"--no-rootfs", "--all", "-c", "4", "-p", "16"}
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

if curl -fsSL --retry 2 --connect-timeout 6 https://tcpquality.ibsgss.uk/run -o "$tmp"; then
    export TCPQUALITY_RAW_BASE="https://raw.githubusercontent.com/ibsgss/TcpQuality/main"
elif curl -fsSL --retry 2 --connect-timeout 6 https://raw.githubusercontent.com/ibsgss/TcpQuality/main/runTcpQuality.sh -o "$tmp"; then
    export TCPQUALITY_RAW_BASE="https://raw.githubusercontent.com/ibsgss/TcpQuality/main"
elif curl -fsSL --retry 2 --connect-timeout 6 https://ghproxy.net/https://raw.githubusercontent.com/ibsgss/TcpQuality/main/runTcpQuality.sh -o "$tmp"; then
    export TCPQUALITY_RAW_BASE="https://ghproxy.net/https://raw.githubusercontent.com/ibsgss/TcpQuality/main"
else
    echo "Failed to download TcpQuality script from all sources" >&2
    exit 1
fi

chmod 0755 "$tmp"
bash "$tmp" %s
exit $?
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
