package control

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Fail2BanJail struct {
	ID              string `json:"id,omitempty"`
	NodeID          string `json:"node_id,omitempty"`
	Name            string `json:"name"`
	LogPath         string `json:"log_path"`
	FilterName      string `json:"filter_name"`
	FailRegex       string `json:"fail_regex"`
	MaxRetry        uint16 `json:"max_retry"`
	FindTimeSeconds uint32 `json:"find_time_seconds"`
	BanTimeSeconds  uint32 `json:"ban_time_seconds"`
	Enabled         bool   `json:"enabled"`
}

// fail2banNamePattern restricts jail and filter names to a safe character set
// so they can be embedded in INI sections and managed file names.
var fail2banNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const fail2banFilterPrefix = "sb-control-"

// SingBoxLogPath is where compiled configurations tell sing-box to write its
// log. Fail2Ban jails watch this file, so both sides must agree on it.
const SingBoxLogPath = "/var/log/sing-box/sing-box.log"

func validateFail2BanJail(jail Fail2BanJail) error {
	if !fail2banNamePattern.MatchString(jail.Name) {
		return errors.New("invalid fail2ban jail name")
	}
	if !fail2banNamePattern.MatchString(jail.FilterName) {
		return errors.New("invalid fail2ban filter name")
	}
	// Jails always target Linux agents, so require a Unix absolute path even
	// when the master itself runs elsewhere.
	if !strings.HasPrefix(jail.LogPath, "/") || strings.ContainsAny(jail.LogPath, "\r\n") {
		return errors.New("fail2ban jail requires an absolute log path")
	}
	if jail.FailRegex == "" || strings.ContainsAny(jail.FailRegex, "\r\n") {
		return errors.New("fail2ban jail requires a single-line fail regex")
	}
	if jail.MaxRetry == 0 || jail.FindTimeSeconds == 0 || jail.BanTimeSeconds == 0 {
		return errors.New("fail2ban jail retry and time values are required")
	}
	return nil
}

// CompileFail2Ban renders the managed jail configuration and one filter file
// per referenced filter name. Filter files live in the sb-control- namespace so
// the agent never touches operator-authored fail2ban configuration.
func CompileFail2Ban(jails []Fail2BanJail) (string, map[string]string, error) {
	var jailOutput strings.Builder
	filters := map[string]string{}
	filterSource := map[string]string{}
	for _, jail := range jails {
		if !jail.Enabled {
			continue
		}
		if err := validateFail2BanJail(jail); err != nil {
			return "", nil, err
		}
		if existing, ok := filterSource[jail.FilterName]; ok && existing != jail.FailRegex {
			return "", nil, fmt.Errorf("fail2ban filter %q is declared twice with different regexes", jail.FilterName)
		}
		filterSource[jail.FilterName] = jail.FailRegex
		managedFilter := fail2banFilterPrefix + jail.FilterName
		filters[managedFilter+".conf"] = "[Definition]\nfailregex = " + jail.FailRegex + "\n"
		jailOutput.WriteString("[" + fail2banFilterPrefix + jail.Name + "]\n")
		jailOutput.WriteString("enabled = true\n")
		jailOutput.WriteString("filter = " + managedFilter + "\n")
		jailOutput.WriteString("logpath = " + jail.LogPath + "\n")
		jailOutput.WriteString(fmt.Sprintf("maxretry = %d\nfindtime = %d\nbantime = %d\n\n", jail.MaxRetry, jail.FindTimeSeconds, jail.BanTimeSeconds))
	}
	return jailOutput.String(), filters, nil
}
