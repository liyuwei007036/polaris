package control

import (
	"fmt"
	"net/http"
	"strings"
)

func sanitizeASCIIFilename(name, fallback string) string {
	ascii := strings.Trim(strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return r
		}
		return '-'
	}, name), "-")
	for strings.Contains(ascii, "--") {
		ascii = strings.ReplaceAll(ascii, "--", "-")
	}
	if ascii == "" {
		return fallback
	}
	return ascii
}

func requestWebURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.Host
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		strings.EqualFold(r.Header.Get("CF-Visitor"), `{"scheme":"https"}`) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/", scheme, host)
}
