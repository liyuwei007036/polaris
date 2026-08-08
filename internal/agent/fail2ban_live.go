package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Automatic banning is read back from the host for the same reason the
// firewall is: a jail file that exists somewhere else proves nothing about
// whether Fail2Ban is actually watching a log and banning anyone.

// fail2banNamePattern restricts jail and filter names to a safe character set
// so they can be embedded in INI sections and managed file names.
var fail2banNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// fail2banPortsPattern keeps the port list to digits, ranges and commas so it
// cannot break out of the generated INI value.
var fail2banPortsPattern = regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?(,[0-9]{1,5}(:[0-9]{1,5})?)*$`)

// LiveFail2BanJail is one jail as it stands on the host, combining what its
// configuration says with what Fail2Ban reports about it at runtime.
type LiveFail2BanJail struct {
	// Name is what an operator sees. For jails this platform wrote it is the
	// name without the polaris- prefix; for the operator's own jails it is
	// exactly what Fail2Ban calls them.
	Name    string `json:"name"`
	Managed bool   `json:"managed"`
	// The configuration fields are read back from the managed jail file, so
	// they are present only for jails this platform wrote.
	LogPath         string `json:"log_path,omitempty"`
	FilterName      string `json:"filter_name,omitempty"`
	FailRegex       string `json:"fail_regex,omitempty"`
	MaxRetry        uint16 `json:"max_retry,omitempty"`
	FindTimeSeconds uint32 `json:"find_time_seconds,omitempty"`
	BanTimeSeconds  uint32 `json:"ban_time_seconds,omitempty"`
	Ports           string `json:"ports,omitempty"`
	// Running says whether Fail2Ban has this jail loaded. A jail that is
	// configured but not running protects nobody, and that difference is the
	// whole reason this is read from the host.
	Running         bool          `json:"running"`
	CurrentlyBanned string        `json:"currently_banned,omitempty"`
	TotalBanned     string        `json:"total_banned,omitempty"`
	Banned          []Fail2BanBan `json:"banned,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// LiveFail2Ban is every jail the host has right now.
type LiveFail2Ban struct {
	Available bool               `json:"available"`
	Jails     []LiveFail2BanJail `json:"jails"`
	Error     string             `json:"error,omitempty"`
}

// Fail2BanMutation is one change to the managed jails.
type Fail2BanMutation struct {
	Operation string           `json:"operation"` // "save" | "delete"
	Jail      LiveFail2BanJail `json:"jail"`
}

// CollectLiveFail2Ban reads the host's jails back: the managed ones from the
// files this platform owns, every jail's runtime state from Fail2Ban itself.
func CollectLiveFail2Ban(ctx context.Context) LiveFail2Ban {
	if !commandExists("fail2ban-client") {
		return LiveFail2Ban{Jails: []LiveFail2BanJail{}}
	}
	live := LiveFail2Ban{Available: true, Jails: []LiveFail2BanJail{}}
	configured, err := readManagedJails()
	if err != nil {
		live.Error = err.Error()
		return live
	}
	runtime := map[string]Fail2BanJailStatus{}
	if report := CollectFail2BanStatus(ctx); report != nil {
		for _, jail := range report.Jails {
			runtime[jail.Name] = jail
		}
	}
	for _, jail := range configured {
		status, running := runtime[fail2banManagedPrefix+jail.Name]
		jail.Running = running
		jail.CurrentlyBanned, jail.TotalBanned, jail.Banned, jail.Error = status.CurrentlyBanned, status.TotalBanned, status.Banned, status.Error
		live.Jails = append(live.Jails, jail)
	}
	// Jails the operator set up outside this platform are listed too, so nobody
	// configures a second rule for protection the server already has.
	for name, status := range runtime {
		if strings.HasPrefix(name, fail2banManagedPrefix) {
			continue
		}
		live.Jails = append(live.Jails, LiveFail2BanJail{
			Name: name, Running: true, CurrentlyBanned: status.CurrentlyBanned,
			TotalBanned: status.TotalBanned, Banned: status.Banned, Error: status.Error,
		})
	}
	sort.SliceStable(live.Jails, func(left, right int) bool {
		if live.Jails[left].Managed != live.Jails[right].Managed {
			return live.Jails[left].Managed
		}
		return live.Jails[left].Name < live.Jails[right].Name
	})
	return live
}

// readManagedJails parses the jail file this platform owns, plus the filter
// file each jail names, back into the fields an operator entered.
func readManagedJails() ([]LiveFail2BanJail, error) {
	content, err := os.ReadFile(managedFail2BanJail)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.New("读取自动封禁配置失败：" + err.Error())
	}
	var jails []LiveFail2BanJail
	var current *LiveFail2BanJail
	flush := func() {
		if current != nil {
			jails = append(jails, *current)
			current = nil
		}
	}
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if !strings.HasPrefix(section, fail2banManagedPrefix) {
				continue
			}
			current = &LiveFail2BanJail{Name: strings.TrimPrefix(section, fail2banManagedPrefix), Managed: true}
			continue
		}
		if current == nil {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "filter":
			current.FilterName = strings.TrimPrefix(value, fail2banManagedPrefix)
			current.FailRegex = readManagedFilterRegex(value)
		case "logpath":
			current.LogPath = value
		case "port":
			current.Ports = value
		case "maxretry":
			if parsed, err := strconv.ParseUint(value, 10, 16); err == nil {
				current.MaxRetry = uint16(parsed)
			}
		case "findtime":
			if parsed, err := strconv.ParseUint(value, 10, 32); err == nil {
				current.FindTimeSeconds = uint32(parsed)
			}
		case "bantime":
			if parsed, err := strconv.ParseUint(value, 10, 32); err == nil {
				current.BanTimeSeconds = uint32(parsed)
			}
		}
	}
	flush()
	return jails, nil
}

func readManagedFilterRegex(filterName string) string {
	content, err := os.ReadFile(filepath.Join(managedFail2BanFilterDir, filterName+".conf"))
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if key, value, found := strings.Cut(line, "="); found && strings.TrimSpace(key) == "failregex" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ApplyFail2BanMutation changes one managed jail on the host and answers with
// what the host runs afterwards. As with the firewall, the node reads its own
// configuration first so the change lands on reality.
func ApplyFail2BanMutation(ctx context.Context, mutation Fail2BanMutation) (LiveFail2Ban, error) {
	current, err := readManagedJails()
	if err != nil {
		return LiveFail2Ban{}, err
	}
	next, err := applyJailMutation(current, mutation)
	if err != nil {
		return LiveFail2Ban{}, err
	}
	jailConfiguration, filters, err := CompileManagedFail2Ban(next)
	if err != nil {
		return LiveFail2Ban{}, err
	}
	if err := writeManagedFail2Ban(ctx, jailConfiguration, filters); err != nil {
		return LiveFail2Ban{}, err
	}
	return CollectLiveFail2Ban(ctx), nil
}

func applyJailMutation(jails []LiveFail2BanJail, mutation Fail2BanMutation) ([]LiveFail2BanJail, error) {
	target := mutation.Jail
	switch mutation.Operation {
	case "save":
		if err := ValidateManagedJail(target); err != nil {
			return nil, err
		}
		target.Managed = true
		next := make([]LiveFail2BanJail, 0, len(jails)+1)
		replaced := false
		for _, jail := range jails {
			if jail.Name == target.Name {
				next = append(next, target)
				replaced = true
				continue
			}
			next = append(next, jail)
		}
		if !replaced {
			next = append(next, target)
		}
		return next, nil
	case "delete":
		next := make([]LiveFail2BanJail, 0, len(jails))
		found := false
		for _, jail := range jails {
			if jail.Name == target.Name {
				found = true
				continue
			}
			next = append(next, jail)
		}
		if !found {
			return nil, errors.New("服务器上已经没有这条自动封禁规则了")
		}
		return next, nil
	default:
		return nil, errors.New("不支持的自动封禁操作")
	}
}

// ValidateManagedJail rejects anything that cannot be safely written into the
// generated INI files or that Fail2Ban would refuse.
func ValidateManagedJail(jail LiveFail2BanJail) error {
	if !fail2banNamePattern.MatchString(jail.Name) {
		return errors.New("规则名称只能使用字母、数字、下划线和短横线")
	}
	if !fail2banNamePattern.MatchString(jail.FilterName) {
		return errors.New("检测器名称只能使用字母、数字、下划线和短横线")
	}
	if !strings.HasPrefix(jail.LogPath, "/") || strings.ContainsAny(jail.LogPath, "\r\n") {
		return errors.New("检查的日志文件必须是绝对路径")
	}
	if jail.FailRegex == "" || strings.ContainsAny(jail.FailRegex, "\r\n") {
		return errors.New("失败记录匹配规则必须是单行内容")
	}
	if jail.MaxRetry == 0 || jail.FindTimeSeconds == 0 || jail.BanTimeSeconds == 0 {
		return errors.New("失败次数与时间设置都必须大于 0")
	}
	if jail.Ports != "" && !fail2banPortsPattern.MatchString(jail.Ports) {
		return errors.New("封禁范围必须是用逗号分隔的端口或端口范围")
	}
	return nil
}

// CompileManagedFail2Ban renders the managed jail file and one filter file per
// referenced filter name. Filter files live in the polaris- namespace so the
// operator's own Fail2Ban configuration is never touched.
func CompileManagedFail2Ban(jails []LiveFail2BanJail) (string, map[string]string, error) {
	var output strings.Builder
	filters := map[string]string{}
	filterSource := map[string]string{}
	for _, jail := range jails {
		if err := ValidateManagedJail(jail); err != nil {
			return "", nil, err
		}
		if existing, ok := filterSource[jail.FilterName]; ok && existing != jail.FailRegex {
			return "", nil, fmt.Errorf("检测器 %q 被两条规则用不同的匹配内容定义了", jail.FilterName)
		}
		filterSource[jail.FilterName] = jail.FailRegex
		managedFilter := fail2banManagedPrefix + jail.FilterName
		filters[managedFilter+".conf"] = "[Definition]\nfailregex = " + jail.FailRegex + "\n"
		output.WriteString("[" + fail2banManagedPrefix + jail.Name + "]\n")
		output.WriteString("enabled = true\n")
		output.WriteString("filter = " + managedFilter + "\n")
		output.WriteString("logpath = " + jail.LogPath + "\n")
		// A ban has to actually reach the kernel. Fail2Ban still defaults to
		// iptables, which on an nftables-only host silently bans nothing at
		// all — the jail counts failures and reports them while every blocked
		// address keeps connecting. This project manages the firewall with
		// nftables, so the ban action has to match.
		//
		// Without a port list the intent is "this address may not connect at
		// all", which is the allports action: multiport with a full range
		// would still only cover TCP and UDP.
		//
		// The protocol list matters just as much: Fail2Ban's nftables actions
		// default to TCP only, so a "blocked" address could still reach every
		// UDP service — including the QUIC-based proxies this tool manages.
		if ports := strings.TrimSpace(jail.Ports); ports == "" {
			output.WriteString("banaction = nftables-allports\nbanaction_allports = nftables-allports\n")
		} else {
			output.WriteString("banaction = nftables-multiport\nbanaction_allports = nftables-allports\n")
			output.WriteString("port = " + ports + "\n")
		}
		output.WriteString("protocol = tcp,udp\n")
		output.WriteString(fmt.Sprintf("maxretry = %d\nfindtime = %d\nbantime = %d\n\n", jail.MaxRetry, jail.FindTimeSeconds, jail.BanTimeSeconds))
	}
	return output.String(), filters, nil
}

// writeManagedFail2Ban replaces the managed files and restarts Fail2Ban,
// restoring the previous files if the new ones are rejected.
func writeManagedFail2Ban(ctx context.Context, jailConfiguration string, filters map[string]string) error {
	if err := ensureFail2BanReady(ctx, jailConfiguration); err != nil {
		return errors.New("准备自动封禁服务失败：" + err.Error())
	}
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
		return errors.New("读取现有自动封禁配置失败：" + err.Error())
	}
	existing, err := filepath.Glob(filepath.Join(managedFail2BanFilterDir, "polaris-*.conf"))
	if err != nil {
		return errors.New("列出现有检测器失败：" + err.Error())
	}
	seen := map[string]bool{managedFail2BanJail: true}
	for _, path := range existing {
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := snapshot(path); err != nil {
			return errors.New("读取现有检测器失败：" + err.Error())
		}
	}
	for name := range filters {
		path := filepath.Join(managedFail2BanFilterDir, name)
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := snapshot(path); err != nil {
			return errors.New("读取现有检测器失败：" + err.Error())
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
		return errors.New("创建自动封禁配置目录失败：" + err.Error() + permissionHint(err))
	}
	if err := os.MkdirAll(managedFail2BanFilterDir, 0o755); err != nil {
		return errors.New("创建检测器目录失败：" + err.Error() + permissionHint(err))
	}
	if err := writeFileAtomic(managedFail2BanJail, []byte(jailConfiguration), 0o640); err != nil {
		restore()
		return errors.New("写入自动封禁配置失败：" + err.Error() + permissionHint(err))
	}
	for name, content := range filters {
		if !managedFilterPattern.MatchString(name) {
			restore()
			return errors.New("检测器文件名超出受管命名空间")
		}
		if err := writeFileAtomic(filepath.Join(managedFail2BanFilterDir, name), []byte(content), 0o640); err != nil {
			restore()
			return errors.New("写入检测器失败：" + err.Error() + permissionHint(err))
		}
	}
	for _, path := range existing {
		if _, keep := filters[filepath.Base(path)]; keep {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			restore()
			return errors.New("清理旧检测器失败：" + err.Error())
		}
	}

	if output, err := exec.CommandContext(ctx, "fail2ban-client", "-t").CombinedOutput(); err != nil {
		if restore() {
			return errors.New(commandSummary("fail2ban-client -t", output, err) + "；已恢复原有配置")
		}
		return errors.New(commandSummary("fail2ban-client -t", output, err) + "；恢复原有配置未完成")
	}
	// A restart, not a reload. Reloading re-reads the jails but never runs a
	// ban action's actionstart, so the nftables table and chain the bans go
	// into are never created: Fail2Ban then reports addresses as banned while
	// the kernel keeps letting them straight through.
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "fail2ban.service").CombinedOutput(); err != nil {
		if restore() {
			_, _ = exec.CommandContext(ctx, "systemctl", "restart", "fail2ban.service").CombinedOutput()
			return errors.New(commandSummary("systemctl restart fail2ban.service", output, err) + "；已恢复原有配置")
		}
		return errors.New(commandSummary("systemctl restart fail2ban.service", output, err) + "；恢复原有配置未完成")
	}
	return nil
}
