package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var managedFail2BanJail = managedSystemPath("/etc/fail2ban/jail.d/polaris.local")
var managedFail2BanFilterDir = managedSystemPath("/etc/fail2ban/filter.d")

// logPathPattern extracts the log files a compiled jail watches. Fail2Ban
// refuses to start a jail whose logpath does not exist, so the agent creates
// each one before validating the configuration.
var logPathPattern = regexp.MustCompile(`(?m)^logpath\s*=\s*(\S+)\s*$`)

// managedFilterPattern accepts only master-compiled filter file names inside
// the polaris namespace; anything else is rejected before touching disk.
var managedFilterPattern = regexp.MustCompile(`^polaris-[a-zA-Z0-9_-]{1,64}\.conf$`)

// fail2banManagedPrefix marks the jails polaris owns. Everything outside
// it belongs to the operator and is only ever read, never written.
const fail2banManagedPrefix = "polaris-"

type fail2banBackup struct {
	path     string
	content  []byte
	existed  bool
	tempName string
}

// ensureFail2BanReady installs Fail2Ban when it is missing and creates every
// log file the jail configuration watches. Without this the very first
// publish failed at `fail2ban-client -t` on any host that had never installed
// Fail2Ban, or on any jail whose log file sing-box had not created yet.
func ensureFail2BanReady(ctx context.Context, jailConfiguration string) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" {
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
		// step before polaris has had a chance to write its own files.
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
	temporary, err := os.CreateTemp(directory, ".polaris-*")
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
			// Jails the operator set up outside polaris are reported too,
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
	status.Banned = collectJailBans(ctx, jail, status.BannedIPs)
	return status
}

// banTimeLayout is how Fail2Ban prints ban and unban times: local wall clock
// with no zone, which is why they are converted to UTC here rather than being
// passed along as text.
const banTimeLayout = "2006-01-02 15:04:05"

// collectJailBans asks Fail2Ban for the ban times of the addresses a jail
// currently holds. Older Fail2Ban releases do not support the flag; the
// addresses from the plain status output are then reported without times
// rather than not at all.
func collectJailBans(ctx context.Context, jail string, bannedIPs []string) []Fail2BanBan {
	output, err := exec.CommandContext(ctx, "fail2ban-client", "get", jail, "banip", "--with-time").CombinedOutput()
	if err != nil {
		return bansWithoutTimes(bannedIPs)
	}
	bans := make([]Fail2BanBan, 0, len(bannedIPs))
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// `IP<tab>2024-01-01 10:00:00 + 3600 = 2024-01-01 11:00:00`
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ban := Fail2BanBan{IP: fields[0]}
		if len(fields) >= 3 {
			ban.BannedAt = parseBanTime(fields[1] + " " + fields[2])
		}
		if len(fields) >= 8 {
			ban.UnbanAt = parseBanTime(fields[6] + " " + fields[7])
		}
		bans = append(bans, ban)
	}
	if len(bans) == 0 {
		return bansWithoutTimes(bannedIPs)
	}
	return bans
}

func bansWithoutTimes(bannedIPs []string) []Fail2BanBan {
	if len(bannedIPs) == 0 {
		return nil
	}
	bans := make([]Fail2BanBan, 0, len(bannedIPs))
	for _, ip := range bannedIPs {
		bans = append(bans, Fail2BanBan{IP: ip})
	}
	return bans
}

func parseBanTime(value string) string {
	parsed, err := time.ParseInLocation(banTimeLayout, strings.TrimSpace(value), time.Local)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

// unbanAddress releases one address from one jail. Both values are checked
// before use so a malformed task can never turn into an unexpected
// fail2ban-client invocation.
func unbanAddress(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Jail string `json:"jail"`
		IP   string `json:"ip"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return TaskResult{Status: "failed", Summary: "invalid fail2ban unban payload"}
	}
	digest := sha256.Sum256([]byte(task.Payload))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		return TaskResult{Status: "failed", Summary: "fail2ban unban payload SHA-256 does not match task hash"}
	}
	if !jailNamePattern.MatchString(payload.Jail) {
		return TaskResult{Status: "failed", Summary: "封禁规则名称无效"}
	}
	if net.ParseIP(payload.IP) == nil {
		return TaskResult{Status: "failed", Summary: "要解封的 IP 地址无效"}
	}
	if !commandExists("fail2ban-client") {
		return TaskResult{Status: "failed", Summary: "该服务器未安装 Fail2Ban，无法解封"}
	}
	output, err := exec.CommandContext(ctx, "fail2ban-client", "set", payload.Jail, "unbanip", payload.IP).CombinedOutput()
	if err != nil {
		return TaskResult{Status: "failed", Summary: commandSummary("fail2ban-client set "+payload.Jail+" unbanip", output, err)}
	}
	return TaskResult{Status: "succeeded", Summary: "已解封 " + payload.IP}
}

// jailNamePattern accepts the jail names Fail2Ban itself allows, covering both
// polaris's own jails and the operator's.
var jailNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)
