// Package selfupdate replaces the running polaris binary with a verified
// release artifact. It only touches the file whose path the caller supplies;
// deciding which release to trust (signed manifest on agents, direct GitHub
// digest on the master) stays with the caller.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

type Manifest struct {
	Version string
	URL     string
	SHA256  string
	Archive string // "tar.gz" or "" for a raw binary
	// FallbackFetch, when set, retrieves the artifact if direct HTTPS
	// download fails or stalls. Apply still enforces the signed SHA-256.
	FallbackFetch func(ctx context.Context, dst io.Writer) error
}

const (
	maximumArtifactBytes  = 200 * 1024 * 1024
	downloadStallTimeout  = 45 * time.Second
	verifyCommandTimeout  = 15 * time.Second
)

// httpClient is a variable so tests can trust their local TLS server.
var httpClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	},
}

// Apply downloads the artifact, checks its SHA-256, extracts the polaris
// binary, backs up executablePath and installs the new binary over it. verify
// runs against the installed file and triggers a rollback when it fails; nil
// selects the default check that "<binary> version" prints manifest.Version.
func Apply(ctx context.Context, manifest Manifest, executablePath string, verify func(ctx context.Context, binaryPath string) error) error {
	if manifest.Version == "" || !strings.HasPrefix(manifest.URL, "https://") || len(manifest.SHA256) != sha256.Size*2 {
		return errors.New("发布信息不完整，无法执行更新")
	}
	if verify == nil {
		verify = func(ctx context.Context, binaryPath string) error {
			verifyCtx, cancel := context.WithTimeout(ctx, verifyCommandTimeout)
			defer cancel()
			output, err := exec.CommandContext(verifyCtx, binaryPath, "version").CombinedOutput()
			if err != nil {
				return fmt.Errorf("运行新版本失败: %w", err)
			}
			if strings.TrimPrefix(strings.TrimSpace(string(output)), "v") != strings.TrimPrefix(strings.TrimSpace(manifest.Version), "v") {
				return fmt.Errorf("新版本报告的版本号 %q 与预期 %q 不符", strings.TrimSpace(string(output)), manifest.Version)
			}
			return nil
		}
	}

	binaryDirectory := filepath.Dir(executablePath)
	CleanStaleArtifacts(binaryDirectory, ".polaris-update-*")
	artifact, err := os.CreateTemp(binaryDirectory, ".polaris-update-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败（进程可能无权写入 %s）: %w", binaryDirectory, err)
	}
	artifactPath := artifact.Name()
	defer os.Remove(artifactPath)

	hash := sha256.New()
	written, downloadErr := downloadDirect(ctx, manifest.URL, artifact, hash, downloadStallTimeout)
	checksumOK := downloadErr == nil && written <= maximumArtifactBytes && strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256)
	if !checksumOK && manifest.FallbackFetch != nil {
		if truncErr := artifact.Truncate(0); truncErr == nil {
			if _, seekErr := artifact.Seek(0, io.SeekStart); seekErr == nil {
				hash.Reset()
				lw := &limitedWriter{w: io.MultiWriter(artifact, hash), limit: maximumArtifactBytes + 1}
				if fbErr := manifest.FallbackFetch(ctx, lw); fbErr == nil {
					written = lw.written
					downloadErr = nil
					checksumOK = written <= maximumArtifactBytes && strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256)
				} else if downloadErr == nil {
					downloadErr = fbErr
				} else {
					downloadErr = fmt.Errorf("%w（备用通道也失败: %v）", downloadErr, fbErr)
				}
			}
		}
	}
	if closeErr := artifact.Close(); downloadErr != nil {
		return downloadErr
	} else if closeErr != nil {
		return fmt.Errorf("关闭新版本文件失败: %w", closeErr)
	}
	if written > maximumArtifactBytes {
		return errors.New("新版本文件超过 200 MiB 限制")
	}
	if !checksumOK {
		return errors.New("新版本文件 SHA-256 校验失败")
	}

	if manifest.Archive == "tar.gz" {
		extractedPath, extractErr := extractBinary(artifactPath, binaryDirectory)
		if extractErr != nil {
			return fmt.Errorf("解包新版本失败: %w", extractErr)
		}
		// 提前清理下载的归档包，立即归还磁盘空间与页缓存
		_ = os.Remove(artifactPath)
		defer os.Remove(extractedPath)
		artifactPath = extractedPath
	} else if manifest.Archive != "" {
		return errors.New("不支持的新版本归档格式")
	}
	if err := os.Chmod(artifactPath, 0o755); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}

	backupPath := executablePath + ".polaris.last-good"
	if err := AtomicBackup(executablePath, backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("备份当前版本失败: %w", err)
	}
	if err := os.Rename(artifactPath, executablePath); err != nil {
		return fmt.Errorf("安装新版本失败（进程可能无权写入 %s）: %w", executablePath, err)
	}

	// 在执行子进程自检前，强制进行GC并向操作系统归还未使用的堆内存
	runtime.GC()
	debug.FreeOSMemory()

	if err := verify(ctx, executablePath); err != nil {
		_ = RestoreBackup(backupPath, executablePath)
		return fmt.Errorf("新版本验证失败，已恢复原版本: %w", err)
	}
	return nil
}

// AtomicBackup 使用硬链接进行秒级备份，避免内存占用与全量磁盘块复制。
// 若跨文件系统等原因导致硬链接失败，则安全降级为流式复制。
func AtomicBackup(src, dst string) error {
	_ = os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst, 0o755)
}

// RestoreBackup 将备份文件原子恢复覆盖回目标路径。
func RestoreBackup(backup, target string) error {
	if err := os.Rename(backup, target); err == nil {
		return nil
	}
	return copyFile(backup, target, 0o755)
}

// CleanStaleArtifacts 清理由于异常中断（如掉电、强杀）可能遗留的历史临时文件。
func CleanStaleArtifacts(directory, pattern string) {
	matches, err := filepath.Glob(filepath.Join(directory, pattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// extractBinary pulls the polaris binary out of the release tar.gz, which
// packages it as polaris_<version>_linux_<arch>/polaris.
func extractBinary(archivePath, directory string) (string, error) {
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
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "polaris" || header.Size <= 0 || header.Size > maximumArtifactBytes {
			continue
		}
		target, err := os.CreateTemp(directory, ".polaris-update-extracted-*")
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
	return "", errors.New("归档中没有 polaris 二进制")
}

type progressReader struct {
	r            io.Reader
	lastProgress *atomic.Int64
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 && p.lastProgress != nil {
		p.lastProgress.Store(time.Now().UnixNano())
	}
	return n, err
}

type limitedWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.written >= l.limit {
		return 0, errors.New("新版本文件超过 200 MiB 限制")
	}
	allowed := p
	if int64(len(allowed)) > l.limit-l.written {
		allowed = allowed[:l.limit-l.written]
	}
	n, err := l.w.Write(allowed)
	l.written += int64(n)
	if err != nil {
		return n, err
	}
	if len(allowed) < len(p) || l.written >= l.limit {
		return n, errors.New("新版本文件超过 200 MiB 限制")
	}
	return n, nil
}

func downloadDirect(ctx context.Context, url string, dst, hash io.Writer, stallTimeout time.Duration) (int64, error) {
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	var stalled atomic.Bool

	done := make(chan struct{})
	if stallTimeout > 0 {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-dlCtx.Done():
					return
				case <-ticker.C:
					if time.Since(time.Unix(0, lastProgress.Load())) > stallTimeout {
						stalled.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}
	defer close(done)

	request, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("构建下载请求失败: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if stalled.Load() {
			return 0, fmt.Errorf("下载新版本超时（连接或传输停滞超过 %s）", stallTimeout)
		}
		return 0, fmt.Errorf("下载新版本失败: %w", err)
	}
	defer response.Body.Close()
	lastProgress.Store(time.Now().UnixNano())
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载新版本返回 HTTP %d", response.StatusCode)
	}

	reader := &progressReader{r: io.LimitReader(response.Body, maximumArtifactBytes+1), lastProgress: &lastProgress}
	written, copyErr := io.Copy(io.MultiWriter(dst, hash), reader)
	if copyErr != nil {
		if stalled.Load() {
			return written, fmt.Errorf("下载新版本超时（传输停滞超过 %s）", stallTimeout)
		}
		return written, fmt.Errorf("写入新版本文件失败: %w", copyErr)
	}
	return written, nil
}
