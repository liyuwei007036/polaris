package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const managedFail2BanJail = "/etc/fail2ban/jail.d/sb-control.local"
const managedFail2BanFilterDir = "/etc/fail2ban/filter.d"

// managedFilterPattern accepts only master-compiled filter file names inside
// the sb-control namespace; anything else is rejected before touching disk.
var managedFilterPattern = regexp.MustCompile(`^sb-control-[a-zA-Z0-9_-]{1,64}\.conf$`)

type fail2banBackup struct {
	path     string
	content  []byte
	existed  bool
	tempName string
}

func applyFail2Ban(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Jail    string            `json:"jail"`
		Filters map[string]string `json:"filters"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return TaskResult{Status: "failed", Summary: "invalid fail2ban payload"}
	}
	digest := sha256.Sum256([]byte(task.Payload))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "fail2ban payload SHA-256 does not match task hash"}
	}
	for name := range payload.Filters {
		if !managedFilterPattern.MatchString(name) {
			return TaskResult{Status: "failed", Summary: "fail2ban filter name is outside the managed namespace"}
		}
	}

	// Snapshot every file this task may change so a failed reload can restore
	// the previous state exactly.
	var backups []fail2banBackup
	snapshot := func(path string) error {
		backup := fail2banBackup{path: path}
		content, err := os.ReadFile(path)
		if err == nil {
			backup.existed = true
			backup.content = content
		} else if !os.IsNotExist(err) {
			return err
		}
		backups = append(backups, backup)
		return nil
	}
	if err := snapshot(managedFail2BanJail); err != nil {
		return TaskResult{Status: "failed", Summary: "read current fail2ban jail file: " + err.Error()}
	}
	existingFilters, err := filepath.Glob(filepath.Join(managedFail2BanFilterDir, "sb-control-*.conf"))
	if err != nil {
		return TaskResult{Status: "failed", Summary: "list managed fail2ban filters: " + err.Error()}
	}
	for _, path := range existingFilters {
		if err := snapshot(path); err != nil {
			return TaskResult{Status: "failed", Summary: "read current fail2ban filter: " + err.Error()}
		}
	}
	for name := range payload.Filters {
		path := filepath.Join(managedFail2BanFilterDir, name)
		already := false
		for _, backup := range backups {
			if backup.path == path {
				already = true
				break
			}
		}
		if !already {
			if err := snapshot(path); err != nil {
				return TaskResult{Status: "failed", Summary: "read current fail2ban filter: " + err.Error()}
			}
		}
	}
	restore := func() bool {
		ok := true
		for _, backup := range backups {
			if backup.existed {
				if err := os.WriteFile(backup.path, backup.content, 0o640); err != nil {
					ok = false
				}
			} else if err := os.Remove(backup.path); err != nil && !os.IsNotExist(err) {
				ok = false
			}
		}
		return ok
	}

	if err := os.MkdirAll(filepath.Dir(managedFail2BanJail), 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "create fail2ban jail directory: " + err.Error()}
	}
	if err := os.MkdirAll(managedFail2BanFilterDir, 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "create fail2ban filter directory: " + err.Error()}
	}
	if err := writeFileAtomic(managedFail2BanJail, []byte(payload.Jail), 0o640); err != nil {
		restore()
		return TaskResult{Status: "failed", Summary: "write fail2ban jail file: " + err.Error()}
	}
	for name, content := range payload.Filters {
		if err := writeFileAtomic(filepath.Join(managedFail2BanFilterDir, name), []byte(content), 0o640); err != nil {
			restore()
			return TaskResult{Status: "failed", Summary: "write fail2ban filter: " + err.Error()}
		}
	}
	// Remove managed filters that are no longer part of the desired state.
	for _, path := range existingFilters {
		if _, keep := payload.Filters[filepath.Base(path)]; !keep {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				restore()
				return TaskResult{Status: "failed", Summary: "remove stale fail2ban filter: " + err.Error()}
			}
		}
	}

	if output, err := exec.CommandContext(ctx, "fail2ban-client", "-t").CombinedOutput(); err != nil {
		if restore() {
			return TaskResult{Status: "rolled_back", Summary: commandSummary("fail2ban-client -t", output, err) + "; restored previous fail2ban configuration"}
		}
		return TaskResult{Status: "failed", Summary: commandSummary("fail2ban-client -t", output, err) + "; rollback did not complete"}
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "reload-or-restart", "fail2ban.service").CombinedOutput(); err != nil {
		if restore() {
			_, _ = exec.CommandContext(ctx, "systemctl", "reload-or-restart", "fail2ban.service").CombinedOutput()
			return TaskResult{Status: "rolled_back", Summary: commandSummary("systemctl reload-or-restart fail2ban.service", output, err) + "; restored previous fail2ban configuration"}
		}
		return TaskResult{Status: "failed", Summary: commandSummary("systemctl reload-or-restart fail2ban.service", output, err) + "; rollback did not complete"}
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "fail2ban.service").CombinedOutput(); err != nil {
		if restore() {
			_, _ = exec.CommandContext(ctx, "systemctl", "reload-or-restart", "fail2ban.service").CombinedOutput()
			return TaskResult{Status: "rolled_back", Summary: commandSummary("systemctl is-active fail2ban.service", output, err) + "; restored previous fail2ban configuration"}
		}
		return TaskResult{Status: "failed", Summary: "fail2ban.service is not active after reload and rollback did not complete"}
	}
	return TaskResult{Status: "succeeded", Summary: "fail2ban configuration validated, applied, and fail2ban.service is active"}
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".sb-control-*")
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

// CollectFail2BanStatus reports managed jail status from fail2ban-client. A
// host without fail2ban simply reports nothing; no values are inferred.
func CollectFail2BanStatus(ctx context.Context) *Fail2BanReport {
	output, err := exec.CommandContext(ctx, "fail2ban-client", "status").CombinedOutput()
	if err != nil {
		return nil
	}
	report := &Fail2BanReport{Available: true, Jails: []Fail2BanJailStatus{}}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, "Jail list:") {
			continue
		}
		names := strings.Split(strings.TrimSpace(strings.SplitN(line, "Jail list:", 2)[1]), ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if !strings.HasPrefix(name, "sb-control-") {
				continue
			}
			report.Jails = append(report.Jails, collectJailStatus(ctx, name))
		}
	}
	return report
}

func collectJailStatus(ctx context.Context, jail string) Fail2BanJailStatus {
	status := Fail2BanJailStatus{Name: jail}
	output, err := exec.CommandContext(ctx, "fail2ban-client", "status", jail).CombinedOutput()
	if err != nil {
		status.Error = commandSummary("fail2ban-client status "+jail, output, err)
		return status
	}
	for _, line := range strings.Split(string(output), "\n") {
		switch {
		case strings.Contains(line, "Currently banned:"):
			status.CurrentlyBanned = strings.TrimSpace(strings.SplitN(line, "Currently banned:", 2)[1])
		case strings.Contains(line, "Total banned:"):
			status.TotalBanned = strings.TrimSpace(strings.SplitN(line, "Total banned:", 2)[1])
		case strings.Contains(line, "Banned IP list:"):
			list := strings.TrimSpace(strings.SplitN(line, "Banned IP list:", 2)[1])
			if list != "" {
				status.BannedIPs = strings.Fields(list)
			}
		}
	}
	return status
}
