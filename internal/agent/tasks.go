package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/nginxroute"
	"github.com/liyuwei007036/polaris/internal/selfupdate"
	"github.com/liyuwei007036/polaris/internal/version"
	"golang.org/x/net/proxy"
)

var managedSingBoxConfig = managedSystemPath("/etc/sing-box/config.json")
var managedNginxConfig = managedSystemPath("/etc/nginx/stream-conf.d/polaris.conf")
var managedNginxModuleConfig = managedSystemPath("/etc/nginx/modules-enabled/99-polaris-stream.conf")
var managedNginxMainConfig = managedSystemPath("/etc/nginx/nginx.conf")

const minimumNginxWorkerConnections = 4096
const minimumNginxWorkerOpenFiles = 65535

// singBoxLogDirectory holds the log file compiled configurations write to.
// It must exist before sing-box starts. See control.SingBoxLogPath.
const singBoxLogDirectory = "/var/log/sing-box"

// The managed firewall keeps its own configuration file and unit so the
// host's /etc/nftables.conf is never rewritten.
const managedNftablesConfig = "/etc/polaris/nftables.conf"
const managedNftablesUnit = "/etc/systemd/system/polaris-nftables.service"

var nginxWorkerConnectionsPattern = regexp.MustCompile(`(?m)^([ \t]*worker_connections[ \t]+)([0-9]+)([ \t]*;)`)
var nginxWorkerOpenFilesPattern = regexp.MustCompile(`(?m)^([ \t]*worker_rlimit_nofile[ \t]+)([0-9]+)([ \t]*;)`)

var outboundProbeURL = "https://www.gstatic.com/generate_204"

// managedSystemPath leaves production paths unchanged. The black-box E2E
// suite sets POLARIS_E2E_ROOT in the spawned agent process so the real task
// executor can exercise atomic writes and rollback bookkeeping without
// touching the host's /etc or /usr/local directories.
func managedSystemPath(path string) string {
	root := strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT"))
	if root == "" {
		return path
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path, "/")))
}

// permissionHint appends operator guidance to a raw OS error when the failure
// is caused by insufficient privileges. The agent must be able to write to
// system paths such as /etc/sing-box and /etc/nginx; on a non-root install
// every such write fails with an unexplained "permission denied".
func permissionHint(err error) string {
	if os.IsPermission(err) {
		return "; agent 没有写入权限，需要以 root 身份运行 polaris agent（例如通过 systemd 并设置 User=root），或对该目录授予写权限后重试"
	}
	return ""
}

// ExecuteTask only invokes a fixed local program with fixed arguments. It does
// not accept shell text or a caller-supplied executable path.
func ExecuteTask(ctx context.Context, task Task) TaskResult {
	return executeTask(ctx, task, TaskOptions{})
}

type NginxPassthroughRoute struct {
	ListenAddress  string `yaml:"listen_address"`
	Port           uint16 `yaml:"port"`
	SNI            string `yaml:"sni"`
	BackendAddress string `yaml:"backend_address"`
	BackendPort    uint16 `yaml:"backend_port"`
}

type TaskOptions struct {
	DataDir                string
	NginxPassthroughRoutes []NginxPassthroughRoute
}

// NewTaskHandler binds task execution to the enrolled agent data directory.
// Installation tasks require the release public key received at enrollment.
func NewTaskHandler(dataDir string) TaskHandler {
	return NewTaskHandlerWithOptions(TaskOptions{DataDir: dataDir})
}

func NewTaskHandlerWithOptions(options TaskOptions) TaskHandler {
	executor := newTaskExecutor(options)
	return executor.Handle
}

func executeTask(ctx context.Context, task Task, options TaskOptions) TaskResult {
	switch task.Kind {
	case "singbox.apply_config":
		return applySingBoxConfig(ctx, task)
	case "nginx.apply_config":
		return applyNginxConfig(ctx, task, options.DataDir, options.NginxPassthroughRoutes)
	case "firewall.query":
		return queryFirewall(ctx)
	case "firewall.mutate":
		return mutateFirewall(ctx, task)
	case "fail2ban.query":
		return queryFail2Ban(ctx)
	case "fail2ban.mutate":
		return mutateFail2Ban(ctx, task)
	case "fail2ban.unban":
		return unbanAddress(ctx, task)
	case "singbox.install", "singbox.upgrade":
		return installSingBox(ctx, task, options.DataDir)
	case "agent.upgrade":
		return upgradeAgent(ctx, task, options.DataDir)
	case "outbound.test":
		return testOutbound(ctx, task)
	default:
		return TaskResult{Status: "failed", Summary: "task kind is not implemented by this agent build"}
	}
}

func testOutbound(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Type       string `json:"type"`
		Server     string `json:"server"`
		ServerPort uint16 `json:"server_port"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Server == "" || payload.ServerPort == 0 {
		return TaskResult{Status: "failed", Summary: "出口代理测试参数无效"}
	}
	testContext, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	transport := &http.Transport{}
	address := net.JoinHostPort(payload.Server, fmt.Sprint(payload.ServerPort))
	switch payload.Type {
	case "http":
		proxyURL := &url.URL{Scheme: "http", Host: address}
		if payload.Username != "" {
			proxyURL.User = url.UserPassword(payload.Username, payload.Password)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	case "socks":
		var auth *proxy.Auth
		if payload.Username != "" {
			auth = &proxy.Auth{User: payload.Username, Password: payload.Password}
		}
		dialer, err := proxy.SOCKS5("tcp", address, auth, &net.Dialer{Timeout: 8 * time.Second})
		if err != nil {
			return TaskResult{Status: "failed", Summary: "创建 SOCKS5 测试连接失败：" + err.Error()}
		}
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
				return dialer.Dial(network, address)
			}
		}
	default:
		return TaskResult{Status: "failed", Summary: "不支持该出口代理类型"}
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(testContext, http.MethodGet, outboundProbeURL, nil)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "创建出口测试请求失败：" + err.Error()}
	}
	started := time.Now()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "出口代理不可用：" + err.Error()}
	}
	defer response.Body.Close()
	latency := time.Since(started).Milliseconds()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return TaskResult{Status: "failed", Summary: fmt.Sprintf("出口代理返回 HTTP %d，延迟 %d ms", response.StatusCode, latency)}
	}
	return TaskResult{Status: "succeeded", Summary: fmt.Sprintf("出口代理可用，HTTP %d，延迟 %d ms", response.StatusCode, latency)}
}

type releaseManifest struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Archive      string `json:"archive,omitempty"`
}

func installSingBox(ctx context.Context, task Task, dataDir string) TaskResult {
	manifest, err := VerifyReleaseTask(dataDir, task)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if manifest.Architecture != runtime.GOARCH {
		return TaskResult{Status: "failed", Summary: "signed sing-box release architecture does not match this agent"}
	}
	binaryPath := managedSystemPath("/usr/local/bin/sing-box")
	if output, err := exec.CommandContext(ctx, binaryPath, "version").CombinedOutput(); err == nil && strings.Contains(string(output), "version "+manifest.Version) && singBoxServiceActive(ctx) {
		return TaskResult{Status: "succeeded", Summary: "requested sing-box version is already active", SingBoxVersion: manifest.Version}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "build sing-box download request: " + err.Error()}
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "download sing-box artifact: " + err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TaskResult{Status: "failed", Summary: fmt.Sprintf("download sing-box artifact returned HTTP %d", response.StatusCode)}
	}

	const maximumArtifactBytes = 200 * 1024 * 1024
	binaryDirectory := filepath.Dir(binaryPath)
	if err := os.MkdirAll(binaryDirectory, 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "create sing-box binary directory: " + err.Error() + permissionHint(err)}
	}
	artifact, err := os.CreateTemp(binaryDirectory, ".polaris-sing-box-*")
	if err != nil {
		return TaskResult{Status: "failed", Summary: "create temporary sing-box artifact: " + err.Error()}
	}
	artifactPath := artifact.Name()
	defer os.Remove(artifactPath)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(artifact, hash), io.LimitReader(response.Body, maximumArtifactBytes+1))
	if closeErr := artifact.Close(); copyErr != nil {
		return TaskResult{Status: "failed", Summary: "write sing-box artifact: " + copyErr.Error()}
	} else if closeErr != nil {
		return TaskResult{Status: "failed", Summary: "close sing-box artifact: " + closeErr.Error()}
	}
	if written > maximumArtifactBytes {
		return TaskResult{Status: "failed", Summary: "sing-box artifact exceeds 200 MiB limit"}
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256) {
		return TaskResult{Status: "failed", Summary: "sing-box artifact SHA-256 does not match"}
	}
	if manifest.Archive == "tar.gz" {
		extractedPath, extractErr := extractSingBoxArchive(artifactPath, binaryDirectory)
		if extractErr != nil {
			return TaskResult{Status: "failed", Summary: "extract sing-box archive: " + extractErr.Error()}
		}
		defer os.Remove(extractedPath)
		artifactPath = extractedPath
	} else if manifest.Archive != "" {
		return TaskResult{Status: "failed", Summary: "unsupported sing-box archive format"}
	}
	if err := os.Chmod(artifactPath, 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "set sing-box artifact mode: " + err.Error()}
	}

	backupPath := binaryPath + ".polaris.last-good"
	if current, err := os.ReadFile(binaryPath); err == nil {
		if err := os.WriteFile(backupPath, current, 0o755); err != nil {
			return TaskResult{Status: "failed", Summary: "backup sing-box binary: " + err.Error()}
		}
	} else if !os.IsNotExist(err) {
		return TaskResult{Status: "failed", Summary: "read sing-box binary: " + err.Error()}
	}
	if err := os.Rename(artifactPath, binaryPath); err != nil {
		return TaskResult{Status: "failed", Summary: "install sing-box binary: " + err.Error()}
	}
	if output, err := exec.CommandContext(ctx, binaryPath, "version").CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "version "+manifest.Version) {
		if backup, readErr := os.ReadFile(backupPath); readErr == nil {
			_ = os.WriteFile(binaryPath, backup, 0o755)
		}
		return TaskResult{Status: "rolled_back", Summary: "sing-box version verification failed; restored prior binary"}
	}
	if result := restartSingBox(ctx); result.Status == "succeeded" {
		return TaskResult{Status: "succeeded", Summary: "sing-box artifact verified, installed, and service is active", SingBoxVersion: manifest.Version}
	}
	if backup, err := os.ReadFile(backupPath); err == nil {
		if os.WriteFile(binaryPath, backup, 0o755) == nil {
			_ = restartSingBox(ctx)
			return TaskResult{Status: "rolled_back", Summary: "sing-box service failed after upgrade; restored prior binary"}
		}
	}
	return TaskResult{Status: "failed", Summary: "sing-box service failed after upgrade and rollback did not complete"}
}

// upgradeAgent replaces the running polaris binary with a master-signed
// release. It reuses the sing-box release manifest verification, so the agent
// still never accepts a bare download URL from the control stream. On success
// the session loop re-executes the process after the result is reported.
func upgradeAgent(ctx context.Context, task Task, dataDir string) TaskResult {
	manifest, err := VerifyReleaseTask(dataDir, task)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if manifest.Architecture != runtime.GOARCH {
		return TaskResult{Status: "failed", Summary: "signed agent release architecture does not match this agent"}
	}
	if manifest.Version == version.Version {
		return TaskResult{Status: "succeeded", Summary: "agent 已是版本 " + manifest.Version + "，无需更新"}
	}
	executable, err := os.Executable()
	if err != nil {
		return TaskResult{Status: "failed", Summary: "定位当前可执行文件失败: " + err.Error()}
	}
	update := selfupdate.Manifest{Version: manifest.Version, URL: manifest.URL, SHA256: manifest.SHA256, Archive: manifest.Archive}
	if err := selfupdate.Apply(ctx, update, executable, nil); err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	return TaskResult{Status: "succeeded", Summary: "agent 已更新到版本 " + manifest.Version + "，正在重启", RestartAgent: true}
}

func extractSingBoxArchive(archivePath, directory string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "sing-box" || header.Size <= 0 || header.Size > 200*1024*1024 {
			continue
		}
		target, err := os.CreateTemp(directory, ".polaris-sing-box-extracted-*")
		if err != nil {
			return "", err
		}
		targetPath := target.Name()
		if _, err := io.CopyN(target, reader, header.Size); err != nil {
			target.Close()
			os.Remove(targetPath)
			return "", err
		}
		if err := target.Close(); err != nil {
			os.Remove(targetPath)
			return "", err
		}
		return targetPath, nil
	}
	return "", errors.New("archive does not contain sing-box binary")
}

// VerifyReleaseTask validates the signed, master-controlled artifact manifest
// before a download is attempted. It is deliberately independent of the task
// stream TLS identity, preventing a malformed task from becoming a binary URL.
func VerifyReleaseTask(dataDir string, task Task) (releaseManifest, error) {
	if dataDir == "" {
		return releaseManifest{}, errors.New("agent release signing key is unavailable")
	}
	var payload struct {
		Manifest  string `json:"manifest"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Manifest == "" || payload.Signature == "" {
		return releaseManifest{}, errors.New("invalid signed sing-box release payload")
	}
	publicKeyPEM, err := os.ReadFile(filepath.Join(dataDir, releaseSigningPublicKeyFile))
	if err != nil {
		return releaseManifest{}, fmt.Errorf("read release signing public key: %w", err)
	}
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil || block.Type != "PUBLIC KEY" {
		return releaseManifest{}, errors.New("release signing public key has invalid PEM format")
	}
	publicValue, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("parse release signing public key: %w", err)
	}
	publicKey, ok := publicValue.(ed25519.PublicKey)
	if !ok {
		return releaseManifest{}, errors.New("release signing public key is not Ed25519")
	}
	signature, err := base64.StdEncoding.DecodeString(payload.Signature)
	if err != nil || !ed25519.Verify(publicKey, []byte(payload.Manifest), signature) {
		return releaseManifest{}, errors.New("sing-box release manifest signature is invalid")
	}
	var manifest releaseManifest
	if err := json.Unmarshal([]byte(payload.Manifest), &manifest); err != nil ||
		!strings.HasPrefix(manifest.URL, "https://") || manifest.Version == "" ||
		len(manifest.SHA256) != sha256.Size*2 || !strings.EqualFold(task.ExpectedHash, manifest.SHA256) {
		return releaseManifest{}, errors.New("signed sing-box release manifest is invalid")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return releaseManifest{}, errors.New("signed sing-box release manifest SHA-256 is invalid")
	}
	if manifest.Architecture != "amd64" && manifest.Architecture != "arm64" {
		return releaseManifest{}, errors.New("signed sing-box release manifest architecture is invalid")
	}
	return manifest, nil
}

// queryFirewall answers with what the host is enforcing right now. Nothing is
// stored anywhere: every time the console shows access limits, it is showing
// the answer to one of these.
func queryFirewall(ctx context.Context) TaskResult {
	live := CollectLiveFirewall(ctx)
	encoded, err := json.Marshal(live)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码防火墙规则失败：" + err.Error()}
	}
	return TaskResult{Status: "succeeded", Summary: fmt.Sprintf("已读取服务器防火墙规则（%d 条）", len(live.Rules)), Data: string(encoded)}
}

// queryFail2Ban answers with the jails the host actually runs.
func queryFail2Ban(ctx context.Context) TaskResult {
	live := CollectLiveFail2Ban(ctx)
	encoded, err := json.Marshal(live)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码自动封禁规则失败：" + err.Error()}
	}
	return TaskResult{Status: "succeeded", Summary: fmt.Sprintf("已读取服务器自动封禁规则（%d 条）", len(live.Jails)), Data: string(encoded)}
}

// mutateFail2Ban changes the host's jails and answers with what it runs
// afterwards, so a rule is only reported as saved once Fail2Ban accepted it.
func mutateFail2Ban(ctx context.Context, task Task) TaskResult {
	var mutation Fail2BanMutation
	if err := json.Unmarshal([]byte(task.Payload), &mutation); err != nil {
		return TaskResult{Status: "failed", Summary: "自动封禁操作参数无效"}
	}
	digest := sha256.Sum256([]byte(task.Payload))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "自动封禁操作校验失败"}
	}
	live, err := ApplyFail2BanMutation(ctx, mutation)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	encoded, err := json.Marshal(live)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码自动封禁规则失败：" + err.Error()}
	}
	return TaskResult{Status: "succeeded", Summary: "自动封禁规则已在服务器上生效", Data: string(encoded)}
}

// mutateFirewall changes the host's firewall and answers with what it enforces
// afterwards, so the console never reports a change it cannot see in place.
func mutateFirewall(ctx context.Context, task Task) TaskResult {
	var mutation FirewallMutation
	if err := json.Unmarshal([]byte(task.Payload), &mutation); err != nil {
		return TaskResult{Status: "failed", Summary: "访问限制操作参数无效"}
	}
	digest := sha256.Sum256([]byte(task.Payload))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "访问限制操作校验失败"}
	}
	live, err := ApplyFirewallMutation(ctx, mutation)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	encoded, err := json.Marshal(live)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码防火墙规则失败：" + err.Error()}
	}
	return TaskResult{Status: "succeeded", Summary: "访问限制已在服务器防火墙上生效", Data: string(encoded)}
}

// replaceableNftablesScript wraps a table definition so loading it replaces
// whatever is there. The empty declaration makes the delete safe when the
// table does not exist yet.
func replaceableNftablesScript(configuration string) string {
	return "table inet polaris\ndelete table inet polaris\n" + configuration
}

func ensureNftablesReady(ctx context.Context) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" || commandExists("nft") {
		return nil
	}
	return installPackages(ctx, "nftables")
}

// persistNftables records the managed table in its own file and unit so the
// rules come back after a reboot. Rules loaded with `nft -f` alone live only
// in the running kernel, which meant every restart silently dropped the whole
// firewall. The host's own /etc/nftables.conf is left untouched.
func persistNftables(ctx context.Context, script string) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" {
		return nil
	}
	configurationPath := managedSystemPath(managedNftablesConfig)
	if err := os.MkdirAll(filepath.Dir(configurationPath), 0o755); err != nil {
		return errors.New("创建防火墙配置目录失败：" + err.Error() + permissionHint(err))
	}
	if err := writeFileAtomic(configurationPath, []byte(script), 0o640); err != nil {
		return errors.New("写入防火墙配置失败：" + err.Error() + permissionHint(err))
	}
	if !commandExists("systemctl") {
		return nil
	}
	unitPath := managedSystemPath(managedNftablesUnit)
	unit := "[Unit]\nDescription=polaris managed nftables rules\nAfter=network-pre.target\nWants=network-pre.target\n\n" +
		"[Service]\nType=oneshot\nRemainAfterExit=yes\n" +
		"ExecStart=/usr/sbin/nft -f " + managedNftablesConfig + "\n" +
		"ExecStop=/usr/sbin/nft delete table inet polaris\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
	existing, readErr := os.ReadFile(unitPath)
	if readErr != nil || string(existing) != unit {
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
			return errors.New("创建 systemd 目录失败：" + err.Error() + permissionHint(err))
		}
		if err := writeFileAtomic(unitPath, []byte(unit), 0o644); err != nil {
			return errors.New("写入开机恢复配置失败：" + err.Error() + permissionHint(err))
		}
		if output, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return errors.New(commandSummary("systemctl daemon-reload", output, err))
		}
	}
	// enable without --now: the rules are already loaded, and starting the
	// unit here would only load them a second time.
	if output, err := exec.CommandContext(ctx, "systemctl", "enable", "polaris-nftables.service").CombinedOutput(); err != nil {
		return errors.New(commandSummary("systemctl enable polaris-nftables.service", output, err))
	}
	return nil
}

func applyNginxConfig(ctx context.Context, task Task, dataDir string, passthrough []NginxPassthroughRoute) (result TaskResult) {
	var payload struct {
		Configuration string `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return TaskResult{Status: "failed", Summary: "invalid Nginx configuration payload"}
	}
	digest := sha256.Sum256([]byte(payload.Configuration))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "Nginx configuration SHA-256 does not match task hash"}
	}
	routes := make([]nginxroute.Route, 0, len(passthrough))
	for _, route := range passthrough {
		routes = append(routes, nginxroute.Route{
			ListenAddress: route.ListenAddress, Port: route.Port, SNI: route.SNI,
			BackendAddress: route.BackendAddress, BackendPort: route.BackendPort,
		})
	}
	// Sites moved aside by an earlier deploy no longer hold the public port, so
	// they cannot be rediscovered below. Their routes come from the record kept
	// at the time, or rebuilding this file would take those sites offline.
	known := loadNginxTakeover(dataDir)
	routes = append(routes, takeoverRecordRoutes(known)...)
	effectiveConfiguration, err := nginxroute.MergePassthrough(payload.Configuration, routes)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "merge Nginx passthrough routes: " + err.Error()}
	}
	if effectiveConfiguration != "" {
		effectiveConfiguration, err = applyTakeoverDefaults(effectiveConfiguration, known)
		if err != nil {
			return TaskResult{Status: "failed", Summary: "恢复现有 Nginx 站点的默认转发失败：" + err.Error()}
		}
	}
	if err := ensureNginxReady(ctx, effectiveConfiguration != ""); err != nil {
		return TaskResult{Status: "failed", Summary: "prepare automatic TCP port routing: " + err.Error()}
	}
	// An Nginx that was installed before polaris may still serve a site on the
	// port the router needs. Moving that site to loopback and routing its own
	// names back to it lets both keep working on the same port.
	var edits []nginxSiteEdit
	defer func() {
		if len(edits) > 0 && result.Status != "succeeded" {
			restoreNginxSites(edits)
		}
	}()
	edits, err = takeOverManagedPorts(ctx, effectiveConfiguration, known)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if len(edits) > 0 {
		effectiveConfiguration, err = nginxroute.MergePassthrough(effectiveConfiguration, takeOverRoutes(edits))
		if err != nil {
			return TaskResult{Status: "failed", Summary: "合并现有 Nginx 站点路由失败：" + err.Error()}
		}
		effectiveConfiguration, err = applyTakeoverDefaults(effectiveConfiguration, takeOverDefaults(edits))
		if err != nil {
			return TaskResult{Status: "failed", Summary: "设置现有 Nginx 站点的默认转发失败：" + err.Error()}
		}
	}
	// A stream server the takeover above could not move is still on the socket
	// the router needs; the duplicate listen would only fail `nginx -t` and
	// roll everything back, so name the file instead.
	if conflicts := foreignStreamConflicts(ctx, effectiveConfiguration); len(conflicts) > 0 {
		return TaskResult{Status: "failed", Summary: "端口冲突：" + strings.Join(conflicts, "；") + "，且无法自动转移。请手动调整该配置，或为接入服务改用其他端口"}
	}
	// Anything still holding a router port is not Nginx and cannot be moved, so
	// say who it is now rather than letting Nginx fail to bind later.
	if owners := foreignPortOwners(ctx, effectiveConfiguration); len(owners) > 0 {
		return TaskResult{Status: "failed", Summary: "端口冲突：" + strings.Join(owners, "；") + "。请停止占用该端口的程序，或为接入服务改用其他端口"}
	}
	effectiveDigest := sha256.Sum256([]byte(effectiveConfiguration))
	if current, err := os.ReadFile(managedNginxConfig); err == nil {
		currentDigest := sha256.Sum256(current)
		if currentDigest == effectiveDigest && nginxServiceActive(ctx) {
			if testOutput, testErr := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); testErr != nil {
				return TaskResult{Status: "failed", Summary: commandSummary("nginx -t", testOutput, testErr)}
			}
			if reloadOutput, reloadErr := exec.CommandContext(ctx, "systemctl", "reload", "nginx.service").CombinedOutput(); reloadErr != nil {
				return TaskResult{Status: "failed", Summary: commandSummary("systemctl reload nginx.service", reloadOutput, reloadErr)}
			}
			return nginxApplySucceeded(dataDir, edits)
		}
	}
	directory := filepath.Dir(managedNginxConfig)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return TaskResult{Status: "failed", Summary: "create Nginx stream directory: " + err.Error() + permissionHint(err)}
	}
	temporary, err := os.CreateTemp(directory, ".polaris-stream-*.conf")
	if err != nil {
		return TaskResult{Status: "failed", Summary: "create temporary Nginx configuration: " + err.Error()}
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(effectiveConfiguration); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: "write temporary Nginx configuration: " + err.Error()}
	}
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: "set temporary Nginx configuration mode: " + err.Error()}
	}
	if err := temporary.Close(); err != nil {
		return TaskResult{Status: "failed", Summary: "close temporary Nginx configuration: " + err.Error()}
	}
	backupPath := managedNginxConfig + ".polaris.last-good"
	if current, err := os.ReadFile(managedNginxConfig); err == nil {
		if err := os.WriteFile(backupPath, current, 0o640); err != nil {
			return TaskResult{Status: "failed", Summary: "backup current Nginx configuration: " + err.Error()}
		}
	} else if !os.IsNotExist(err) {
		return TaskResult{Status: "failed", Summary: "read current Nginx configuration: " + err.Error()}
	}
	if err := os.Rename(temporaryPath, managedNginxConfig); err != nil {
		return TaskResult{Status: "failed", Summary: "atomically replace Nginx configuration: " + err.Error()}
	}
	if effectiveConfiguration == "" && !nginxServiceActive(ctx) {
		return TaskResult{Status: "succeeded", Summary: "managed Nginx configuration cleared; inactive service left stopped"}
	}
	var applyErr string
	if testOutput, testErr := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); testErr != nil {
		applyErr = commandSummary("nginx -t", testOutput, testErr)
	} else if nginxServiceActive(ctx) {
		if reloadOutput, reloadErr := exec.CommandContext(ctx, "systemctl", "reload", "nginx.service").CombinedOutput(); reloadErr != nil {
			applyErr = commandSummary("systemctl reload nginx.service", reloadOutput, reloadErr)
		}
	} else if startOutput, startErr := exec.CommandContext(ctx, "systemctl", "enable", "--now", "nginx.service").CombinedOutput(); startErr != nil {
		applyErr = commandSummary("systemctl enable --now nginx.service", startOutput, startErr)
	} else {
		return nginxApplySucceeded(dataDir, edits)
	}
	if applyErr == "" {
		return nginxApplySucceeded(dataDir, edits)
	}
	if backup, err := os.ReadFile(backupPath); err == nil {
		if os.WriteFile(managedNginxConfig, backup, 0o640) == nil {
			if _, restoreErr := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); restoreErr == nil {
				_, _ = exec.CommandContext(ctx, "systemctl", "reload", "nginx.service").CombinedOutput()
				return TaskResult{Status: "rolled_back", Summary: "new Nginx configuration failed (" + applyErr + "); restored last successful configuration"}
			}
		}
	}
	return TaskResult{Status: "failed", Summary: "Nginx configuration failed and automatic rollback did not complete: " + applyErr}
}

func ensureNginxReady(ctx context.Context, needed bool) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" {
		return nil
	}
	installedByUs := false
	if _, err := exec.LookPath("nginx"); err != nil {
		if !needed {
			return nil
		}
		packages := []string{"nginx", "nginx-mod-stream"}
		packageManager := ""
		switch {
		case commandExists("apt-get"):
			packageManager = "apt"
			packages = []string{"nginx", "libnginx-mod-stream"}
		case commandExists("dnf"):
			packageManager = "dnf"
		case commandExists("yum"):
			packageManager = "yum"
		case commandExists("apk"):
			packageManager = "apk"
		default:
			return errors.New("未找到 Nginx，也未识别到受支持的软件包管理器；请先安装带 stream 模块的 Nginx")
		}
		maskedByUs := false
		alreadyMasked := false
		if commandExists("systemctl") {
			maskedOutput, _ := exec.CommandContext(ctx, "systemctl", "is-enabled", "nginx.service").CombinedOutput()
			alreadyMasked = strings.Contains(strings.ToLower(string(maskedOutput)), "masked")
			if !alreadyMasked {
				if _, maskErr := exec.CommandContext(ctx, "systemctl", "mask", "nginx.service").CombinedOutput(); maskErr == nil {
					maskedByUs = true
				}
			}
		}
		if packageManager == "apt" && !alreadyMasked && !maskedByUs {
			return errors.New("无法在安装前阻止 Nginx 默认站点启动，已取消安装以避免暴露额外 HTTP 端口")
		}
		unmask := func() {
			if maskedByUs {
				_, _ = exec.CommandContext(ctx, "systemctl", "unmask", "nginx.service").CombinedOutput()
			}
		}
		if err := installPackages(ctx, packages...); err != nil {
			unmask()
			return err
		}
		installedByUs = true
		if commandExists("systemctl") {
			_, _ = exec.CommandContext(ctx, "systemctl", "stop", "nginx.service").CombinedOutput()
		}
		unmask()
		if packageManager == "apt" {
			defaultSite := "/etc/nginx/sites-enabled/default"
			if info, statErr := os.Lstat(defaultSite); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				if removeErr := os.Remove(defaultSite); removeErr != nil {
					return errors.New("关闭 Nginx 默认 HTTP 站点失败: " + removeErr.Error())
				}
			}
		}
	}
	if !needed {
		return nil
	}
	if _, err := ensureNginxWorkerCapacity(ctx); err != nil {
		return err
	}
	fullConfig, fullConfigErr := exec.CommandContext(ctx, "nginx", "-T").CombinedOutput()
	if installedByUs && fullConfigErr == nil && nginxHasHTTPListener(string(fullConfig)) {
		return errors.New("Nginx 配置仍包含 HTTP 监听端口；为避免暴露默认站点，自动端口分配已停止")
	}
	if fullConfigErr == nil && strings.Contains(string(fullConfig), "/etc/nginx/stream-conf.d/*.conf") {
		return nil
	}
	if fullConfigErr == nil && strings.Contains(string(fullConfig), "stream {") {
		return errors.New("现有 Nginx 已配置其他 stream 入口；请在该入口中加入 /etc/nginx/stream-conf.d/*.conf 后重试")
	}
	mainConfig, err := os.ReadFile(managedNginxMainConfig)
	if err != nil {
		return errors.New("读取 Nginx 主配置失败: " + err.Error())
	}
	if strings.Contains(string(mainConfig), "/etc/nginx/stream-conf.d/*.conf") {
		return nil
	}
	if !strings.Contains(string(mainConfig), "/etc/nginx/modules-enabled/*.conf") {
		return errors.New("现有 Nginx 主配置未加载 modules-enabled，无法安全加入自动端口分配配置")
	}
	if !nginxHasStreamSupport(ctx, string(fullConfig)) {
		return errors.New("当前 Nginx 未提供 stream 功能，无法按连接域名自动分配 TCP 端口；请安装 stream 模块（Debian/Ubuntu 为 libnginx-mod-stream，RHEL 系为 nginx-mod-stream）后重试")
	}
	if err := os.MkdirAll(filepath.Dir(managedNginxModuleConfig), 0o755); err != nil {
		return errors.New("创建 Nginx 模块配置目录失败: " + err.Error() + permissionHint(err))
	}
	include := []byte("stream {\n    include /etc/nginx/stream-conf.d/*.conf;\n}\n")
	if err := os.WriteFile(managedNginxModuleConfig, include, 0o644); err != nil {
		return errors.New("写入 Nginx 自动端口配置入口失败: " + err.Error() + permissionHint(err))
	}
	return nil
}

// nginxHasStreamSupport reports whether this Nginx can serve a stream block.
//
// Checking `nginx -V` for --with-stream only recognises a statically compiled
// build. Debian and Ubuntu ship stream as a dynamic module, and their nginx
// binary carries no stream flag at all, so that check rejected hosts where
// the module was installed and already loaded. The loaded configuration is
// the authority; the module file on disk is the fallback for a host that has
// the package but has not enabled it.
func nginxHasStreamSupport(ctx context.Context, loadedConfiguration string) bool {
	if strings.Contains(loadedConfiguration, "ngx_stream_module.so") {
		return true
	}
	if output, err := exec.CommandContext(ctx, "nginx", "-V").CombinedOutput(); err == nil && strings.Contains(string(output), "--with-stream") {
		return true
	}
	for _, candidate := range []string{
		"/usr/lib/nginx/modules/ngx_stream_module.so",
		"/usr/lib64/nginx/modules/ngx_stream_module.so",
		"/usr/share/nginx/modules/ngx_stream_module.so",
	} {
		if _, err := os.Stat(managedSystemPath(candidate)); err == nil {
			return true
		}
	}
	return false
}

func ensureNginxWorkerCapacity(ctx context.Context) (bool, error) {
	mainConfig, err := os.ReadFile(managedNginxMainConfig)
	if err != nil {
		return false, errors.New("读取 Nginx 主配置失败: " + err.Error())
	}
	updated, connectionsChanged := raiseNginxWorkerConnections(mainConfig)
	updated, openFilesChanged := raiseNginxWorkerOpenFiles(updated)
	changed := connectionsChanged || openFilesChanged
	if !changed {
		return false, nil
	}
	info, err := os.Stat(managedNginxMainConfig)
	if err != nil {
		return false, errors.New("读取 Nginx 主配置权限失败: " + err.Error())
	}
	if err := replaceFileAtomically(managedNginxMainConfig, updated, info.Mode().Perm()); err != nil {
		return false, errors.New("提升 Nginx 连接容量失败: " + err.Error() + permissionHint(err))
	}
	if output, testErr := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); testErr != nil {
		if restoreErr := replaceFileAtomically(managedNginxMainConfig, mainConfig, info.Mode().Perm()); restoreErr != nil {
			return false, errors.New(commandSummary("nginx -t", output, testErr) + "; 恢复 Nginx 主配置失败: " + restoreErr.Error())
		}
		return false, errors.New(commandSummary("nginx -t", output, testErr))
	}
	return true, nil
}

func EnsureManagedNginxCapacity(ctx context.Context) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" || !commandExists("nginx") {
		return nil
	}
	configuration, err := os.ReadFile(managedNginxConfig)
	if os.IsNotExist(err) || strings.TrimSpace(string(configuration)) == "" {
		return nil
	}
	if err != nil {
		return errors.New("读取受管 Nginx 配置失败: " + err.Error())
	}
	changed, err := ensureNginxWorkerCapacity(ctx)
	if err != nil || !changed || !nginxServiceActive(ctx) {
		return err
	}
	if output, reloadErr := exec.CommandContext(ctx, "systemctl", "reload", "nginx.service").CombinedOutput(); reloadErr != nil {
		return errors.New(commandSummary("systemctl reload nginx.service", output, reloadErr))
	}
	return nil
}

func raiseNginxWorkerConnections(configuration []byte) ([]byte, bool) {
	matches := nginxWorkerConnectionsPattern.FindAllSubmatchIndex(configuration, 2)
	if len(matches) != 1 {
		return configuration, false
	}
	match := matches[0]
	current, err := strconv.Atoi(string(configuration[match[4]:match[5]]))
	if err != nil || current >= minimumNginxWorkerConnections {
		return configuration, false
	}
	updated := make([]byte, 0, len(configuration)+4)
	updated = append(updated, configuration[:match[4]]...)
	updated = strconv.AppendInt(updated, minimumNginxWorkerConnections, 10)
	updated = append(updated, configuration[match[5]:]...)
	return updated, true
}

func raiseNginxWorkerOpenFiles(configuration []byte) ([]byte, bool) {
	matches := nginxWorkerOpenFilesPattern.FindAllSubmatchIndex(configuration, 2)
	if len(matches) == 0 {
		updated := make([]byte, 0, len(configuration)+32)
		updated = append(updated, "worker_rlimit_nofile 65535;\n"...)
		updated = append(updated, configuration...)
		return updated, true
	}
	if len(matches) != 1 {
		return configuration, false
	}
	match := matches[0]
	current, err := strconv.Atoi(string(configuration[match[4]:match[5]]))
	if err != nil || current >= minimumNginxWorkerOpenFiles {
		return configuration, false
	}
	updated := make([]byte, 0, len(configuration)+4)
	updated = append(updated, configuration[:match[4]]...)
	updated = strconv.AppendInt(updated, minimumNginxWorkerOpenFiles, 10)
	updated = append(updated, configuration[match[5]:]...)
	return updated, true
}

func replaceFileAtomically(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".polaris-nginx-main-*.conf")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func nginxHasHTTPListener(configuration string) bool {
	for _, line := range strings.Split(configuration, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		address := strings.TrimSuffix(fields[1], ";")
		if address == "80" || strings.HasSuffix(address, ":80") {
			return true
		}
	}
	return false
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// installPackages installs packages with whichever package manager the host
// has. `apt-get update` is best-effort on purpose: real hosts routinely carry
// one broken third-party repository, and refusing to continue there meant the
// wanted package never got installed even though every other repository could
// still supply it. Only the install itself decides success, and a failed
// refresh is reported alongside it to explain why.
func installPackages(ctx context.Context, packages ...string) error {
	var command []string
	refresh := false
	switch {
	case commandExists("apt-get"):
		command = append([]string{"apt-get", "install", "-y"}, packages...)
		refresh = true
	case commandExists("dnf"):
		command = append([]string{"dnf", "install", "-y"}, packages...)
	case commandExists("yum"):
		command = append([]string{"yum", "install", "-y"}, packages...)
	case commandExists("apk"):
		command = append([]string{"apk", "add"}, packages...)
	default:
		return fmt.Errorf("未识别到受支持的软件包管理器，请先手动安装：%s", strings.Join(packages, " "))
	}
	refreshWarning := ""
	if refresh {
		if output, err := exec.CommandContext(ctx, "apt-get", "update").CombinedOutput(); err != nil {
			refreshWarning = "；软件源刷新有告警：" + commandSummary("apt-get update", output, err)
		}
	}
	if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
		return errors.New(commandSummary(strings.Join(command, " "), output, err) + refreshWarning)
	}
	return nil
}

func applySingBoxConfig(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Configuration string `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Configuration == "" {
		return TaskResult{Status: "failed", Summary: "invalid sing-box configuration payload"}
	}
	digest := sha256.Sum256([]byte(payload.Configuration))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "configuration SHA-256 does not match task hash"}
	}
	if current, err := os.ReadFile(managedSingBoxConfig); err == nil {
		currentDigest := sha256.Sum256(current)
		if strings.EqualFold(hex.EncodeToString(currentDigest[:]), task.ExpectedHash) && singBoxServiceActive(ctx) {
			return TaskResult{Status: "succeeded", Summary: "requested sing-box configuration is already active"}
		}
	}
	configDir := filepath.Dir(managedSingBoxConfig)
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return TaskResult{Status: "failed", Summary: "create sing-box configuration directory: " + err.Error() + permissionHint(err)}
	}
	// Compiled configurations log to a file so Fail2Ban has something to
	// watch; sing-box refuses to start if that directory does not exist.
	if err := os.MkdirAll(managedSystemPath(singBoxLogDirectory), 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "create sing-box log directory: " + err.Error() + permissionHint(err)}
	}
	temporary, err := os.CreateTemp(configDir, ".polaris-config-*.json")
	if err != nil {
		return TaskResult{Status: "failed", Summary: "create temporary configuration: " + err.Error()}
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(payload.Configuration); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: "write temporary configuration: " + err.Error()}
	}
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: "set temporary configuration mode: " + err.Error()}
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: "sync temporary configuration: " + err.Error()}
	}
	if err := temporary.Close(); err != nil {
		return TaskResult{Status: "failed", Summary: "close temporary configuration: " + err.Error()}
	}
	if output, err := exec.CommandContext(ctx, "sing-box", "check", "-c", temporaryPath).CombinedOutput(); err != nil {
		return TaskResult{Status: "failed", Summary: commandSummary("sing-box check", output, err)}
	}
	// Checked before the configuration is put in place: a port owned by another
	// process only makes sing-box fail on start, and the restart loop that
	// follows is far harder to read than this message.
	if conflicts := singBoxPortConflicts(ctx, payload.Configuration); len(conflicts) > 0 {
		return TaskResult{Status: "failed", Summary: "端口冲突：" + strings.Join(conflicts, "；") + "。请停止占用该端口的程序，或为接入服务改用其他端口"}
	}
	backupPath := managedSingBoxConfig + ".polaris.last-good"
	if current, err := os.ReadFile(managedSingBoxConfig); err == nil {
		if err := os.WriteFile(backupPath, current, 0o640); err != nil {
			return TaskResult{Status: "failed", Summary: "backup current configuration: " + err.Error()}
		}
	} else if !os.IsNotExist(err) {
		return TaskResult{Status: "failed", Summary: "read current configuration: " + err.Error()}
	}
	if err := os.Rename(temporaryPath, managedSingBoxConfig); err != nil {
		return TaskResult{Status: "failed", Summary: "atomically replace configuration: " + err.Error()}
	}
	result := restartSingBox(ctx)
	if result.Status == "succeeded" {
		return result
	}
	if backup, err := os.ReadFile(backupPath); err == nil {
		if restoreErr := os.WriteFile(managedSingBoxConfig, backup, 0o640); restoreErr == nil {
			if rollback := restartSingBox(ctx); rollback.Status == "succeeded" {
				return TaskResult{Status: "rolled_back", Summary: "new configuration failed (" + result.Summary + "); restored last successful configuration"}
			}
		}
	}
	// The specific failure (e.g. sing-box.service missing, or a runtime
	// rejection check didn't catch) must survive here — this is the only
	// message an operator sees, and it is often their very first deploy with
	// no prior backup to roll back to.
	return TaskResult{Status: "failed", Summary: "new configuration failed and automatic rollback did not complete: " + result.Summary}
}

var managedSingBoxUnit = managedSystemPath("/etc/systemd/system/sing-box.service")

const initialSingBoxConfig = `{
  "log": { "level": "info" },
  "inbounds": [],
  "outbounds": [
    { "type": "direct", "tag": "direct" }
  ],
  "route": { "final": "direct" },
  "experimental": {
    "clash_api": { "external_controller": "127.0.0.1:9090" }
  }
}
`

const singBoxUnitContents = `[Unit]
Description=sing-box service (managed by polaris)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/sing-box run -c /etc/sing-box/config.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// ensureSingBoxService installs and enables a systemd unit for sing-box the
// first time one is needed. singbox.install only places the binary; without
// this, "systemctl restart sing-box.service" fails with "unit not found" on
// any node that never had sing-box set up through a package manager, and
// that failure looks like a config or protocol problem rather than a missing
// service definition. An existing unit (e.g. from the official package) is
// left untouched.
func ensureSingBoxService(ctx context.Context) error {
	if _, err := os.Stat(managedSingBoxConfig); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(managedSingBoxConfig), 0o750); err != nil {
			return errors.New("create sing-box configuration directory: " + err.Error() + permissionHint(err))
		}
		if err := os.WriteFile(managedSingBoxConfig, []byte(initialSingBoxConfig), 0o640); err != nil {
			return errors.New("write initial sing-box configuration: " + err.Error() + permissionHint(err))
		}
	} else if err != nil {
		return errors.New("read sing-box configuration: " + err.Error() + permissionHint(err))
	}
	if exec.CommandContext(ctx, "systemctl", "cat", "sing-box.service").Run() == nil {
		return nil
	}
	if err := os.WriteFile(managedSingBoxUnit, []byte(singBoxUnitContents), 0o644); err != nil {
		return errors.New("write sing-box systemd unit: " + err.Error() + permissionHint(err))
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return errors.New(commandSummary("systemctl daemon-reload", output, err))
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "enable", "sing-box.service").CombinedOutput(); err != nil {
		return errors.New(commandSummary("systemctl enable sing-box.service", output, err))
	}
	return nil
}

func restartSingBox(ctx context.Context) TaskResult {
	if err := ensureSingBoxService(ctx); err != nil {
		return TaskResult{Status: "failed", Summary: "prepare sing-box service: " + err.Error()}
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "sing-box.service").CombinedOutput(); err != nil {
		return TaskResult{Status: "failed", Summary: commandSummary("systemctl restart sing-box.service", output, err)}
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "sing-box.service").CombinedOutput(); err != nil {
		return TaskResult{Status: "failed", Summary: commandSummary("systemctl is-active sing-box.service", output, err)}
	}
	return TaskResult{Status: "succeeded", Summary: "configuration validated, replaced, and sing-box.service is active"}
}

func singBoxServiceActive(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "sing-box.service").Run() == nil
}

func nginxServiceActive(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "nginx.service").Run() == nil
}

func commandSummary(name string, output []byte, err error) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 2048 {
		text = text[:2048]
	}
	if text == "" {
		return fmt.Sprintf("%s: %v", name, err)
	}
	return fmt.Sprintf("%s: %v: %s", name, err, text)
}
