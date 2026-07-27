package agent

import (
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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const managedSingBoxConfig = "/etc/sing-box/config.json"
const managedNginxConfig = "/etc/nginx/stream-conf.d/sb-control.conf"

// permissionHint appends operator guidance to a raw OS error when the failure
// is caused by insufficient privileges. The agent must be able to write to
// system paths such as /etc/sing-box and /etc/nginx; on a non-root install
// every such write fails with an unexplained "permission denied".
func permissionHint(err error) string {
	if os.IsPermission(err) {
		return "; agent 没有写入权限，需要以 root 身份运行 sb-control agent（例如通过 systemd 并设置 User=root），或对该目录授予写权限后重试"
	}
	return ""
}

// ExecuteTask only invokes a fixed local program with fixed arguments. It does
// not accept shell text or a caller-supplied executable path.
func ExecuteTask(ctx context.Context, task Task) TaskResult {
	return executeTask(ctx, task, "")
}

// NewTaskHandler binds task execution to the enrolled agent data directory.
// Installation tasks require the release public key received at enrollment.
func NewTaskHandler(dataDir string) TaskHandler {
	executor := newTaskExecutor(dataDir)
	return executor.Handle
}

func executeTask(ctx context.Context, task Task, dataDir string) TaskResult {
	switch task.Kind {
	case "singbox.apply_config":
		return applySingBoxConfig(ctx, task)
	case "nginx.apply_config":
		return applyNginxConfig(ctx, task)
	case "firewall.apply":
		return applyNftables(ctx, task)
	case "fail2ban.apply":
		return applyFail2Ban(ctx, task)
	case "singbox.install", "singbox.upgrade":
		return installSingBox(ctx, task, dataDir)
	default:
		return TaskResult{Status: "failed", Summary: "task kind is not implemented by this agent build"}
	}
}

type releaseManifest struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
}

func installSingBox(ctx context.Context, task Task, dataDir string) TaskResult {
	manifest, err := VerifyReleaseTask(dataDir, task)
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if manifest.Architecture != runtime.GOARCH {
		return TaskResult{Status: "failed", Summary: "signed sing-box release architecture does not match this agent"}
	}
	const binaryPath = "/usr/local/bin/sing-box"
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
	artifact, err := os.CreateTemp(binaryDirectory, ".sb-control-sing-box-*")
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
	if err := os.Chmod(artifactPath, 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "set sing-box artifact mode: " + err.Error()}
	}

	backupPath := binaryPath + ".sb-control.last-good"
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

func applyNftables(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Configuration string `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Configuration == "" {
		return TaskResult{Status: "failed", Summary: "invalid nftables configuration payload"}
	}
	digest := sha256.Sum256([]byte(payload.Configuration))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "nftables configuration SHA-256 does not match task hash"}
	}
	snapshot, _ := exec.CommandContext(ctx, "nft", "list", "table", "inet", "sb_control").CombinedOutput()
	temporary, err := os.CreateTemp("/tmp", "sb-control-nft-*.nft")
	if err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(payload.Configuration); err != nil {
		temporary.Close()
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if err := temporary.Close(); err != nil {
		return TaskResult{Status: "failed", Summary: err.Error()}
	}
	if output, err := exec.CommandContext(ctx, "nft", "-c", "-f", temporaryPath).CombinedOutput(); err != nil {
		return TaskResult{Status: "failed", Summary: commandSummary("nft -c", output, err)}
	}
	_, _ = exec.CommandContext(ctx, "nft", "delete", "table", "inet", "sb_control").CombinedOutput()
	if output, err := exec.CommandContext(ctx, "nft", "-f", temporaryPath).CombinedOutput(); err == nil {
		return TaskResult{Status: "succeeded", Summary: "sb_control nftables table applied"}
	} else {
		_ = output
	}
	if len(snapshot) > 0 {
		if restore, err := os.CreateTemp("/tmp", "sb-control-nft-restore-*.nft"); err == nil {
			restorePath := restore.Name()
			if _, err := restore.Write(snapshot); err == nil {
				restore.Close()
				_, _ = exec.CommandContext(ctx, "nft", "delete", "table", "inet", "sb_control").CombinedOutput()
				_, _ = exec.CommandContext(ctx, "nft", "-f", restorePath).CombinedOutput()
			}
			os.Remove(restorePath)
		}
	}
	return TaskResult{Status: "rolled_back", Summary: "nftables apply failed; attempted restore of sb_control table"}
}

func applyNginxConfig(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Configuration string `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Configuration == "" {
		return TaskResult{Status: "failed", Summary: "invalid Nginx configuration payload"}
	}
	digest := sha256.Sum256([]byte(payload.Configuration))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "Nginx configuration SHA-256 does not match task hash"}
	}
	if current, err := os.ReadFile(managedNginxConfig); err == nil {
		currentDigest := sha256.Sum256(current)
		if strings.EqualFold(hex.EncodeToString(currentDigest[:]), task.ExpectedHash) && nginxServiceActive(ctx) {
			return TaskResult{Status: "succeeded", Summary: "requested Nginx configuration is already active"}
		}
	}
	directory := filepath.Dir(managedNginxConfig)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return TaskResult{Status: "failed", Summary: "create Nginx stream directory: " + err.Error() + permissionHint(err)}
	}
	temporary, err := os.CreateTemp(directory, ".sb-control-stream-*.conf")
	if err != nil {
		return TaskResult{Status: "failed", Summary: "create temporary Nginx configuration: " + err.Error()}
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(payload.Configuration); err != nil {
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
	backupPath := managedNginxConfig + ".sb-control.last-good"
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
	var applyErr string
	if testOutput, testErr := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); testErr != nil {
		applyErr = commandSummary("nginx -t", testOutput, testErr)
	} else if reloadOutput, reloadErr := exec.CommandContext(ctx, "systemctl", "reload", "nginx.service").CombinedOutput(); reloadErr != nil {
		applyErr = commandSummary("systemctl reload nginx.service", reloadOutput, reloadErr)
	} else {
		return TaskResult{Status: "succeeded", Summary: "Nginx configuration validated and reloaded"}
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
	temporary, err := os.CreateTemp(configDir, ".sb-control-config-*.json")
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
	backupPath := managedSingBoxConfig + ".sb-control.last-good"
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

const managedSingBoxUnit = "/etc/systemd/system/sing-box.service"
const singBoxUnitContents = `[Unit]
Description=sing-box service (managed by sb-control)
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
