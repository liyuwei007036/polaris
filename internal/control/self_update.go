package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/selfupdate"
	"github.com/sb-control/sb-control/internal/version"
)

// sbControlReleaseAPI is a variable so tests can point it at a local server.
var sbControlReleaseAPI = "https://api.github.com/repos/liyuwei007036/sb-control/releases/latest"

const sbControlReleaseCacheTTL = time.Hour

type sbControlReleaseCacheEntry struct {
	release   SingBoxRelease
	fetchedAt time.Time
}

// LatestSBControlRelease resolves the newest published sb-control release for
// one architecture from GitHub, reusing the SingBoxRelease manifest shape so
// the existing signing and dispatch path applies unchanged.
func LatestSBControlRelease(ctx context.Context, architecture string) (SingBoxRelease, error) {
	if architecture != "amd64" && architecture != "arm64" {
		return SingBoxRelease{}, errors.New("sb-control 发布仅支持 amd64 或 arm64 架构")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sbControlReleaseAPI, nil)
	if err != nil {
		return SingBoxRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "sb-control")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return SingBoxRelease{}, fmt.Errorf("查询 sb-control 最新版本失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SingBoxRelease{}, fmt.Errorf("sb-control 版本查询返回 HTTP %d", response.StatusCode)
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
		return SingBoxRelease{}, fmt.Errorf("解析 sb-control 版本信息失败: %w", err)
	}
	if document.Draft || document.Prerelease || document.TagName == "" {
		return SingBoxRelease{}, errors.New("sb-control 最新发布不是稳定版本")
	}
	releaseVersion := strings.TrimPrefix(document.TagName, "v")
	name := fmt.Sprintf("sb-control_%s_linux_%s.tar.gz", releaseVersion, architecture)
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
	latestParts, ok := versionParts(strings.TrimPrefix(latest, "v"))
	if !ok {
		return false
	}
	currentParts, ok := versionParts(strings.TrimPrefix(current, "v"))
	if !ok {
		return false
	}
	for i := 0; i < len(latestParts) || i < len(currentParts); i++ {
		l, c := 0, 0
		if i < len(latestParts) {
			l = latestParts[i]
		}
		if i < len(currentParts) {
			c = currentParts[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

func versionParts(value string) ([]int, bool) {
	if value == "" {
		return nil, false
	}
	segments := strings.Split(value, ".")
	parts := make([]int, len(segments))
	for i, segment := range segments {
		number, err := strconv.Atoi(segment)
		if err != nil || number < 0 {
			return nil, false
		}
		parts[i] = number
	}
	return parts, true
}

func (s *Server) latestSBControlRelease(ctx context.Context, architecture string) (SingBoxRelease, error) {
	return s.latestSBControlReleaseCached(ctx, architecture, false)
}

func (s *Server) latestSBControlReleaseCached(ctx context.Context, architecture string, bypassCache bool) (SingBoxRelease, error) {
	s.selfUpdateMu.Lock()
	entry, cached := s.sbControlLatest[architecture]
	s.selfUpdateMu.Unlock()
	if cached && !bypassCache && time.Since(entry.fetchedAt) < sbControlReleaseCacheTTL {
		return entry.release, nil
	}
	resolver := s.latestSBControlReleaseFn
	if resolver == nil {
		resolver = LatestSBControlRelease
	}
	release, err := resolver(ctx, architecture)
	if err != nil {
		return SingBoxRelease{}, err
	}
	s.selfUpdateMu.Lock()
	if s.sbControlLatest == nil {
		s.sbControlLatest = make(map[string]sbControlReleaseCacheEntry)
	}
	s.sbControlLatest[architecture] = sbControlReleaseCacheEntry{release: release, fetchedAt: time.Now()}
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
	release, err := s.latestSBControlReleaseCached(r.Context(), runtime.GOARCH, r.URL.Query().Get("refresh") == "1")
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
	release, err := s.latestSBControlRelease(r.Context(), runtime.GOARCH)
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
	release, err := s.latestSBControlRelease(r.Context(), node.Architecture)
	if err != nil {
		writeError(w, err)
		return
	}
	if node.AgentVersion == release.Version {
		writeError(w, fmt.Errorf("该服务器的 agent 已是最新版本 %s", release.Version))
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
