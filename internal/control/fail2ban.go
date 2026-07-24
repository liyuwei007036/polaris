package control

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type Fail2BanJail struct {
	Name            string `json:"name"`
	LogPath         string `json:"log_path"`
	FilterName      string `json:"filter_name"`
	FailRegex       string `json:"fail_regex"`
	MaxRetry        uint16 `json:"max_retry"`
	FindTimeSeconds uint32 `json:"find_time_seconds"`
	BanTimeSeconds  uint32 `json:"ban_time_seconds"`
	Enabled         bool   `json:"enabled"`
}

func CompileFail2Ban(jails []Fail2BanJail) (string, string, error) {
	var jailOutput, filterOutput strings.Builder
	for _, jail := range jails {
		if !jail.Enabled {
			continue
		}
		if jail.Name == "" || strings.ContainsAny(jail.Name, " /\\\r\n") {
			return "", "", errors.New("invalid fail2ban jail name")
		}
		if !filepath.IsAbs(jail.LogPath) || jail.FilterName == "" || jail.FailRegex == "" {
			return "", "", errors.New("fail2ban jail requires absolute log path, filter name and fail regex")
		}
		if jail.MaxRetry == 0 || jail.FindTimeSeconds == 0 || jail.BanTimeSeconds == 0 {
			return "", "", errors.New("fail2ban jail retry and time values are required")
		}
		jailOutput.WriteString("[")
		jailOutput.WriteString(jail.Name)
		jailOutput.WriteString("]\nenabled = true\nfilter = ")
		jailOutput.WriteString(jail.FilterName)
		jailOutput.WriteString("\nlogpath = ")
		jailOutput.WriteString(jail.LogPath)
		jailOutput.WriteString("\nmaxretry = ")
		jailOutput.WriteString(fmt.Sprint(jail.MaxRetry))
		jailOutput.WriteString("\nfindtime = ")
		jailOutput.WriteString(fmt.Sprint(jail.FindTimeSeconds))
		jailOutput.WriteString("\nbantime = ")
		jailOutput.WriteString(fmt.Sprint(jail.BanTimeSeconds))
		jailOutput.WriteString("\n\n")
		filterOutput.WriteString("[Definition]\nfailregex = ")
		filterOutput.WriteString(jail.FailRegex)
		filterOutput.WriteString("\n\n")
	}
	return jailOutput.String(), filterOutput.String(), nil
}
