package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var managedFail2BanJail = managedSystemPath("/etc/fail2ban/jail.d/sb-control.local")
var managedFail2BanFilterDir = managedSystemPath("/etc/fail2ban/filter.d")

// logPathPattern extracts the log files a compiled jail watches. Fail2Ban
// refuses to start a jail whose logpath does not exist, so the agent creates
// each one before validating the configuration.
var logPathPattern = regexp.MustCompile(`(?m)^logpath\s*=\s*(\S+)\s*$`)

// managedFilterPattern accepts only master-compiled filter file names inside
// the sb-control namespace; anything else is rejected before touching disk.
var managedFilterPattern = regexp.MustCompile(`^sb-control-[a-zA-Z0-9_-]{1,64}\.conf$`)

// fail2banManagedPrefix marks the jails sb-control owns. Everything outside
// it belongs to the operator and is only ever read, never written.
const fail2banManagedPrefix = "sb-control-"

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
	if err := ensureFail2BanReady(ctx, payload.Jail); err != nil {
		return TaskResult{Status: "failed", Summary: "准备自动封禁服务失败：" + err.Error()}
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
		return TaskResult{Status: "failed", Summary: "create fail2ban jail directory: " + err.Error() + permissionHint(err)}
	}
	if err := os.MkdirAll(managedFail2BanFilterDir, 0o755); err != nil {
		return TaskResult{Status: "failed", Summary: "create fail2ban filter directory: " + err.Error() + permissionHint(err)}
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
	// A restart, not a reload. Reloading re-reads the jails but never runs a
	// ban action's actionstart, so the nftables table and chain the bans go
	// into are never created: Fail2Ban then reports addresses as banned while
	// the kernel keeps letting them straight through.
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "fail2ban.service").CombinedOutput(); err != nil {
		if restore() {
			_, _ = exec.CommandContext(ctx, "systemctl", "restart", "fail2ban.service").CombinedOutput()
			return TaskResult{Status: "rolled_back", Summary: commandSummary("systemctl restart fail2ban.service", output, err) + "; restored previous fail2ban configuration"}
		}
		return TaskResult{Status: "failed", Summary: commandSummary("systemctl restart fail2ban.service", output, err) + "; rollback did not complete"}
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "fail2ban.service").CombinedOutput(); err != nil {
		if restore() {
			_, _ = exec.CommandContext(ctx, "systemctl", "restart", "fail2ban.service").CombinedOutput()
			return TaskResult{Status: "rolled_back", Summary: commandSummary("systemctl is-active fail2ban.service", output, err) + "; restored previous fail2ban configuration"}
		}
		return TaskResult{Status: "failed", Summary: "fail2ban.service is not active after reload and rollback did not complete"}
	}
	return TaskResult{Status: "succeeded", Summary: "fail2ban configuration validated, applied, and fail2ban.service is active"}
}

// ensureFail2BanReady installs Fail2Ban when it is missing and creates every
// log file the jail configuration watches. Without this the very first
// publish failed at `fail2ban-client -t` on any host that had never installed
// Fail2Ban, or on any jail whose log file sing-box had not created yet.
func ensureFail2BanReady(ctx context.Context, jailConfiguration string) error {
	if strings.TrimSpace(os.Getenv("SB_CONTROL_E2E_ROOT")) != "" {
		return nil
	}
	if strings.TrimSpace(jailConfiguration) == "" {
		// Clearing every jail needs no service and no log files.
		return nil
	}
	// An install is attempted only when Fail2Ban is genuinely absent. A host
	// that already runs it — very often with the operator's own jails in
	// jail.d — must be left exactly as it is: reinstalling could restart the
	// service and disrupt protection that is already working.
	if err := installFail2BanIfMissing(ctx); err != nil {
		return err
	}
	// The generated jails ban through nftables, so the tool has to be there
	// or every ban would be recorded by Fail2Ban and enforced by nothing.
	if err := ensureNftablesReady(ctx); err != nil {
		return err
	}
	for _, match := range logPathPattern.FindAllStringSubmatch(jailConfiguration, -1) {
		if err := ensureLogFile(managedSystemPath(match[1])); err != nil {
			return err
		}
	}
	if commandExists("systemctl") {
		// Only arm it for boot here. Starting the service is left to the
		// reload-or-restart that follows the configuration write, so a host
		// whose existing configuration is currently broken does not fail this
		// step before sb-control has had a chance to write its own files.
		if output, err := exec.CommandContext(ctx, "systemctl", "enable", "fail2ban.service").CombinedOutput(); err != nil {
			return errors.New(commandSummary("systemctl enable fail2ban.service", output, err))
		}
	}
	return nil
}

func installFail2BanIfMissing(ctx context.Context) error {
	if commandExists("fail2ban-client") {
		return nil
	}
	return installPackages(ctx, "fail2ban")
}

// ensureLogFile creates a watched log file and its directory if absent,
// leaving an existing file untouched.
func ensureLogFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return errors.New("检查日志文件 " + path + " 失败：" + err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.New("创建日志目录失败：" + err.Error() + permissionHint(err))
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return errors.New("创建日志文件 " + path + " 失败：" + err.Error() + permissionHint(err))
	}
	return file.Close()
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
			if name == "" {
				continue
			}
			// Jails the operator set up outside sb-control are reported too,
			// marked as unmanaged, so the console can show what a host is
			// already protected by instead of pretending nothing is there.
			status := collectJailStatus(ctx, name)
			status.Managed = strings.HasPrefix(name, fail2banManagedPrefix)
			report.Jails = append(report.Jails, status)
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
