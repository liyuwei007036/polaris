package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/selfupdate"
	"github.com/liyuwei007036/polaris/internal/version"
)

// polarisReleaseAPI is a variable so tests can point it at a local server.
var polarisReleaseAPI = "https://api.github.com/repos/liyuwei007036/polaris/releases/latest"

// polarisArtifactHTTPClient is a variable so tests can serve release archives locally.
var polarisArtifactHTTPClient = &http.Client{Timeout: 5 * time.Minute}

const polarisReleaseCacheTTL = time.Hour

type polarisReleaseCacheEntry struct {
	release   SingBoxRelease
	fetchedAt time.Time
}

// LatestPolarisRelease resolves the newest published polaris release for
// one architecture from GitHub, reusing the SingBoxRelease manifest shape so
// the existing signing and dispatch path applies unchanged.
func LatestPolarisRelease(ctx context.Context, architecture string) (SingBoxRelease, error) {
	if architecture != "amd64" && architecture != "arm64" {
		return SingBoxRelease{}, errors.New("polaris 发布仅支持 amd64 或 arm64 架构")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, polarisReleaseAPI, nil)
	if err != nil {
		return SingBoxRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "polaris")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return SingBoxRelease{}, fmt.Errorf("查询 polaris 最新版本失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SingBoxRelease{}, fmt.Errorf("polaris 版本查询返回 HTTP %d", response.StatusCode)
	}
	var document struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024))
	if err := decoder.Decode(&document); err != nil {
		return SingBoxRelease{}, fmt.Errorf("解析 polaris 版本信息失败: %w", err)
	}
	if document.Draft || document.Prerelease || document.TagName == "" {
		return SingBoxRelease{}, errors.New("polaris 最新发布不是稳定版本")
	}
	releaseVersion := strings.TrimPrefix(document.TagName, "v")
	name := fmt.Sprintf("polaris_%s_linux_%s.tar.gz", releaseVersion, architecture)
	for _, asset := range document.Assets {
		if asset.Name != name || !strings.HasPrefix(asset.Digest, "sha256:") {
			continue
		}
		release := SingBoxRelease{Version: releaseVersion, Architecture: architecture, URL: asset.URL, SHA256: strings.TrimPrefix(asset.Digest, "sha256:"), Enabled: true, Archive: "tar.gz"}
		if err := validateSingBoxRelease(release); err != nil {
			return SingBoxRelease{}, err
		}
		return release, nil
	}
	return SingBoxRelease{}, fmt.Errorf("版本 %s 没有经过校验的 %s Linux 发布包", releaseVersion, architecture)
}

// IsNewerVersion reports whether latest is a strictly newer dotted-numeric
// version than current. Non-numeric versions ("dev", commit hashes) never
// compare as outdated, so development builds are not prompted to update.
func IsNewerVersion(latest, current string) bool {
	return version.IsNewer(latest, current)
}

func (s *Server) latestPolarisRelease(ctx context.Context, architecture string) (SingBoxRelease, error) {
	return s.latestPolarisReleaseCached(ctx, architecture, false)
}

func (s *Server) latestPolarisReleaseCached(ctx context.Context, architecture string, bypassCache bool) (SingBoxRelease, error) {
	s.selfUpdateMu.Lock()
	if bypassCache && s.polarisLatest != nil {
		// Invalidate all architectures when an explicit refresh is requested so
		// arm64/amd64 nodes do not lag behind the master's refreshed architecture.
		clear(s.polarisLatest)
	}
	entry, cached := s.polarisLatest[architecture]
	s.selfUpdateMu.Unlock()
	if cached && !bypassCache && time.Since(entry.fetchedAt) < polarisReleaseCacheTTL {
		return entry.release, nil
	}
	resolver := s.latestPolarisReleaseFn
	if resolver == nil {
		resolver = LatestPolarisRelease
	}
	release, err := resolver(ctx, architecture)
	if err != nil {
		return SingBoxRelease{}, err
	}
	s.selfUpdateMu.Lock()
	if s.polarisLatest == nil {
		s.polarisLatest = make(map[string]polarisReleaseCacheEntry)
	}
	for arch, prev := range s.polarisLatest {
		if arch != architecture && prev.release.Version != release.Version {
			delete(s.polarisLatest, arch)
		}
	}
	s.polarisLatest[architecture] = polarisReleaseCacheEntry{release: release, fetchedAt: time.Now()}
	s.selfUpdateMu.Unlock()
	return release, nil
}

// systemUpdateStatus reports the running master version and the newest
// published release. A failed GitHub lookup is reported in-band so the
// console can render normally while offline.
func (s *Server) systemUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	status := map[string]any{
		"current_version":  version.Version,
		"latest_version":   "",
		"update_available": false,
		"os":               runtime.GOOS,
		"architecture":     runtime.GOARCH,
	}
	release, err := s.latestPolarisReleaseCached(r.Context(), runtime.GOARCH, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		status["check_error"] = err.Error()
	} else {
		status["latest_version"] = release.Version
		status["update_available"] = IsNewerVersion(release.Version, version.Version)
	}
	writeJSON(w, http.StatusOK, status)
}

// applySystemUpdate downloads the newest release, swaps the master binary and
// re-executes the process. systemd sees the same PID throughout, so this works
// for master, agent and combined units alike.
func (s *Server) applySystemUpdate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if runtime.GOOS != "linux" {
		writeError(w, errors.New("master 自动更新仅支持 Linux"))
		return
	}
	release, err := s.latestPolarisRelease(r.Context(), runtime.GOARCH)
	if err != nil {
		writeError(w, err)
		return
	}
	if !IsNewerVersion(release.Version, version.Version) {
		writeError(w, fmt.Errorf("当前版本 %s 已是最新，无需更新", version.Version))
		return
	}
	executable, err := os.Executable()
	if err != nil {
		writeError(w, fmt.Errorf("定位当前可执行文件失败: %w", err))
		return
	}
	apply := s.selfUpdateApplyFn
	if apply == nil {
		apply = selfupdate.Apply
	}
	manifest := selfupdate.Manifest{Version: release.Version, URL: release.URL, SHA256: release.SHA256, Archive: release.Archive}
	if err := apply(r.Context(), manifest, executable, nil); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "system.self_update", "system", "", "master updated to version "+release.Version+"; restarting"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting", "version": release.Version})
	restart := s.selfUpdateRestartFn
	if restart == nil {
		restart = selfupdate.Restart
	}
	go func() {
		// Give the HTTP response a moment to flush before exec replaces the
		// process image.
		time.Sleep(time.Second)
		if err := restart(); err != nil {
			fmt.Fprintln(os.Stderr, "restart after self-update failed:", err)
		}
	}()
}

// upgradeNodeAgent dispatches a signed agent.upgrade task, following the same
// signed-manifest path as sing-box installations: the agent never accepts a
// bare download URL from the control stream.
func (s *Server) upgradeNodeAgent(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	node, err := s.store.GetNode(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if node.OS != "" && node.OS != "linux" {
		writeError(w, errors.New("agent 自动更新仅支持 Linux 节点"))
		return
	}
	if node.Architecture == "" {
		writeError(w, errors.New("尚未获取该节点的架构信息，请等待 agent 心跳后重试"))
		return
	}
	release, err := s.latestPolarisReleaseCached(r.Context(), node.Architecture, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, err)
		return
	}
	currentVersion := strings.TrimPrefix(strings.TrimSpace(node.AgentVersion), "v")
	if currentVersion == release.Version {
		writeError(w, fmt.Errorf("该服务器的 agent 已是最新版本 %s", release.Version))
		return
	}
	if activeTask, active, prepErr := s.store.PrepareAgentUpgradeTask(r.Context(), nodeID, release.SHA256, 15*time.Second); prepErr != nil {
		writeError(w, prepErr)
		return
	} else if active {
		writeJSON(w, http.StatusAccepted, activeTask)
		return
	}
	payload, err := s.store.SignedSingBoxReleasePayload(release)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.DispatchTask(r.Context(), Task{
		NodeID: nodeID, OperatorID: operator.ID, Kind: "agent.upgrade",
		IdempotencyKey: "agent-upgrade-" + release.Version + "-" + release.SHA256,
		Payload:        payload, ExpectedHash: release.SHA256,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "agent.upgrade_requested", "node", nodeID, "signed agent upgrade task requested for version "+release.Version); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

const maximumRelayedArtifactBytes = 200 * 1024 * 1024

// ensurePolarisArtifact returns the local path to a verified release tarball
// cached under <data_dir>/release-cache so agents without direct GitHub
// connectivity can fetch the signed release through the master Noise/KCP channel.
func (s *Server) ensurePolarisArtifact(ctx context.Context, architecture, targetVersion, expectedSHA256 string) (string, error) {
	if architecture != "amd64" && architecture != "arm64" {
		return "", errors.New("unsupported artifact architecture")
	}
	targetVersion = strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if targetVersion == "" || len(expectedSHA256) != sha256HexLength {
		return "", errors.New("invalid artifact request")
	}
	release, err := s.latestPolarisRelease(ctx, architecture)
	if err != nil {
		return "", err
	}
	if release.Version != targetVersion || !strings.EqualFold(release.SHA256, expectedSHA256) {
		release, err = s.latestPolarisReleaseCached(ctx, architecture, true)
		if err != nil {
			return "", err
		}
		if release.Version != targetVersion || !strings.EqualFold(release.SHA256, expectedSHA256) {
			return "", errors.New("requested artifact does not match an approved release")
		}
	}
	cacheDir := filepath.Join(s.store.DataDir(), "release-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create release cache directory: %w", err)
	}
	cacheName := fmt.Sprintf("polaris_%s_linux_%s_%s.tar.gz", release.Version, release.Architecture, expectedSHA256)
	cachePath := filepath.Join(cacheDir, cacheName)

	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()

	if fileSHA256Matches(cachePath, expectedSHA256) {
		return cachePath, nil
	}
	_ = os.Remove(cachePath)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build release download request: %w", err)
	}
	response, err := polarisArtifactHTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("download release archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download release archive returned HTTP %d", response.StatusCode)
	}

	tempFile, err := os.CreateTemp(cacheDir, ".polaris-cache-*")
	if err != nil {
		return "", fmt.Errorf("create release cache temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tempFile, hash), io.LimitReader(response.Body, maximumRelayedArtifactBytes+1))
	closeErr := tempFile.Close()
	if copyErr != nil {
		return "", fmt.Errorf("write release cache file: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close release cache file: %w", closeErr)
	}
	if written > maximumRelayedArtifactBytes {
		return "", errors.New("release archive exceeds 200 MiB limit")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return "", errors.New("release archive SHA-256 verification failed")
	}

	if entries, listErr := os.ReadDir(cacheDir); listErr == nil {
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || name == filepath.Base(tempPath) {
				continue
			}
			// Keep at most the current release per architecture; drop stale archives.
			if strings.Contains(name, "_linux_"+release.Architecture+"_") {
				_ = os.Remove(filepath.Join(cacheDir, name))
			}
		}
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		return "", fmt.Errorf("commit release cache file: %w", err)
	}
	return cachePath, nil
}

func fileSHA256Matches(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected)
}
