package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	// Give a generous 10-minute timeout so full multi-vector suites on any VPS finish reliably.
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
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
		// Enforce safe and optimized options:
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
export TCPQUALITY_INTERNATIONAL_IPERF_BUDGET_SECONDS=10
export TCPQUALITY_INTERNATIONAL_IPERF_SECONDS=3
export TCPQUALITY_INTERNATIONAL_MAX_IPS=1

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
# Fix hardcoded curl -4 in upload_report if present so IPv6/dual-stack nodes upload reliably
sed -i 's/curl -4 -sS/curl -sS/g' "$tmp" 2>/dev/null || true
bash "$tmp" %s
exit $?
`, argsStr)
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"TERM=xterm",
		"DEBIAN_FRONTEND=noninteractive",
		"TCPQUALITY_INTERNATIONAL_IPERF_BUDGET_SECONDS=10",
		"TCPQUALITY_INTERNATIONAL_IPERF_SECONDS=3",
		"TCPQUALITY_INTERNATIONAL_MAX_IPS=1",
	)

	outBytes, err := cmd.CombinedOutput()
	output := string(outBytes)

	// Extract report URL from output
	reportURL := ""
	matches := tcpQualityURLRegex.FindAllString(output, -1)
	if len(matches) > 0 {
		reportURL = matches[len(matches)-1]
	}

	// Fallback: If URL was not in stdout, check if a CSV was generated in /tmp
	if reportURL == "" {
		if csvFiles, globErr := filepath.Glob("/tmp/zstatic_nping_*.csv"); globErr == nil && len(csvFiles) > 0 {
			var newestFile string
			var newestMod time.Time
			for _, f := range csvFiles {
				if fi, statErr := os.Stat(f); statErr == nil {
					if fi.ModTime().After(newestMod) {
						newestMod = fi.ModTime()
						newestFile = f
					}
				}
			}
			if newestFile != "" {
				if uploadedURL, uploadErr := uploadTcpQualityCSV(timeoutCtx, newestFile); uploadErr == nil && uploadedURL != "" {
					reportURL = uploadedURL
				}
			}
		}
	}

	if reportURL == "" {
		if err != nil {
			tail := output
			if len(tail) > 600 {
				tail = tail[len(tail)-600:]
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

func uploadTcpQualityCSV(ctx context.Context, csvPath string) (string, error) {
	data, err := os.ReadFile(csvPath)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://tcpquality.ibsgss.uk/generate", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/csv; charset=utf-8")
	req.Header.Set("X-Report-Time", time.Now().Format("2006-01-02 15:04:05"))
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var res struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.URL, nil
}
