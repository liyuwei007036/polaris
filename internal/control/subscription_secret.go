package control

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

// A subscription may be fenced three ways: a secret the client has to present,
// a time of day outside which it stays silent, and a moment after which it
// retires. All three are optional and independent; a configuration with none
// of them behaves exactly as it did before they existed.
//
// The secret travels in the User-Agent because that is the only request header
// the common Clash/Mihomo front-ends let an operator set on a subscription. It
// is a second segment of the same credential rather than a second factor: it
// leaks with the address whenever a whole profile leaks. What it stops is the
// common accident — an address forwarded on its own, opened in a browser, or
// pasted into an online tool.
const subscriptionSecretPrefix = "polaris/"

// Built from the prefix so the two never drift apart; the character class is
// the alphabet a secret is held to below.
var subscriptionSecretPattern = regexp.MustCompile(regexp.QuoteMeta(subscriptionSecretPrefix) + `([A-Za-z0-9_-]+)`)

// A secret has to survive being pulled back out of a User-Agent, so it is
// restricted to the alphabet the pattern recognises.
var subscriptionSecretAlphabet = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// subscriptionUserAgent renders the header value handed to the operator.
func subscriptionUserAgent(secret string) string {
	if secret == "" {
		return ""
	}
	return subscriptionSecretPrefix + secret
}

// normalizeAccessSecret accepts an operator-supplied secret, or none at all.
func normalizeAccessSecret(secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", nil
	}
	if !subscriptionSecretAlphabet.MatchString(secret) {
		return "", userErrorf("访问密钥只能包含字母、数字、下划线和短横线，长度 8-64 位")
	}
	return secret, nil
}

// userAgentCarriesSecret reports whether the header presents the secret behind
// hash. It compares hashes in constant time, so verifying never needs the
// secret itself.
func userAgentCarriesSecret(userAgent string, hash []byte) bool {
	for _, match := range subscriptionSecretPattern.FindAllStringSubmatch(userAgent, -1) {
		if constantTimeEqual(security.TokenHash(match[1]), hash) {
			return true
		}
	}
	return false
}

// redactSubscriptionSecret strips the secret before the header reaches the
// access log. That trail is stored in the clear and shown in the console, so
// leaving the secret in would put it on screen beside the address it guards.
// Every candidate is redacted, not just the correct one, so a wrong guess is
// not recorded either.
func redactSubscriptionSecret(userAgent string) string {
	return subscriptionSecretPattern.ReplaceAllString(userAgent, subscriptionSecretPrefix+"***")
}

// unsetAccessWindowBound marks a window edge the operator left blank.
const unsetAccessWindowBound = -1

// parseAccessWindowBound turns "HH:MM" into minutes past midnight.
func parseAccessWindowBound(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return unsetAccessWindowBound, nil
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, userErrorf("访问时间段需要 HH:MM 格式，例如 08:00")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func formatAccessWindowBound(minutes int) string {
	if minutes < 0 || minutes > 24*60 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func parseAccessExpiry(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, userErrorf("有效期需要 RFC3339 时间格式")
	}
	return parsed.Unix(), nil
}

func formatAccessExpiry(at int64) string {
	if at == 0 {
		return ""
	}
	return time.Unix(at, 0).UTC().Format(time.RFC3339)
}

// withinAccessWindow reports whether at falls inside the configured window. A
// start past the end spans midnight, so 22:00-06:00 covers the night rather
// than covering nothing.
func withinAccessWindow(at time.Time, start, end int) bool {
	if start == unsetAccessWindowBound || end == unsetAccessWindowBound {
		return true
	}
	minutes := at.Hour()*60 + at.Minute()
	if start <= end {
		return minutes >= start && minutes <= end
	}
	return minutes >= start || minutes <= end
}

// mihomoSubscriptionAccess is everything a pull is checked against once its
// address has resolved to a configuration.
type mihomoSubscriptionAccess struct {
	ConfigID    string
	SecretHash  []byte
	WindowStart int
	WindowEnd   int
	ExpiresAt   int64
}

// permits reports whether a pull carrying userAgent at the moment at may go
// ahead. Every rejection looks the same to the caller, so a probe cannot tell
// a wrong secret from a closed window or a retired subscription.
func (a mihomoSubscriptionAccess) permits(userAgent string, at time.Time) bool {
	if len(a.SecretHash) != 0 && !userAgentCarriesSecret(userAgent, a.SecretHash) {
		return false
	}
	if a.ExpiresAt != 0 && !at.Before(time.Unix(a.ExpiresAt, 0)) {
		return false
	}
	return withinAccessWindow(at, a.WindowStart, a.WindowEnd)
}
