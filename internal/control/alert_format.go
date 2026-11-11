package control

import (
	"fmt"
	"html"
	"strings"
)

// alertPayload carries channel-specific message content. Bark is a compact
// lock-screen notification, while Telegram is rendered as a detailed bot
// message with structured fields and an optional suggested action.
type alertPayload struct {
	Severity  string
	BarkTitle string
	BarkBody  string
	BarkGroup string
	BarkSound string
	Telegram  string
}

const (
	severityCritical = "紧急"
	severityWarning  = "警告"
	severityInfo     = "信息"
	severityRecovery = "恢复"
)

func severityLabel(severity string) string {
	switch severity {
	case severityCritical:
		return "紧急告警"
	case severityWarning:
		return "警告"
	case severityRecovery:
		return "恢复"
	default:
		return "信息"
	}
}

func tgEscape(value string) string {
	return html.EscapeString(strings.TrimSpace(value))
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func tgLine(label, value string) string {
	return fmt.Sprintf("<b>%s</b>：%s", tgEscape(label), tgEscape(value))
}

func tgCodeLine(label, value string) string {
	return fmt.Sprintf("<b>%s</b>：<code>%s</code>", tgEscape(label), tgEscape(value))
}

func tgLinkLine(label, href, text string) string {
	return fmt.Sprintf("<b>%s</b>：<a href=\"%s\">%s</a>",
		tgEscape(label), html.EscapeString(strings.TrimSpace(href)), tgEscape(text))
}

// buildTelegramAlert renders a detailed bot-style HTML message. Every dynamic
// value passed through tgLine/tgCodeLine/tgLinkLine is escaped for Telegram HTML.
func buildTelegramAlert(severity, category, headline, subject string, lines []string, action string) string {
	return buildTelegramAlertFor("管理通知", severity, category, headline, subject, lines, action)
}

func buildTelegramAlertFor(audience, severity, category, headline, subject string, lines []string, action string) string {
	var b strings.Builder
	b.WriteString("<b>Polaris ")
	b.WriteString(tgEscape(audience))
	b.WriteString(" · ")
	b.WriteString(tgEscape(severityLabel(severity)))
	b.WriteString("</b>\n")
	b.WriteString("<b>")
	b.WriteString(tgEscape(category))
	if strings.TrimSpace(headline) != "" {
		b.WriteString(" / ")
		b.WriteString(tgEscape(headline))
	}
	b.WriteString("</b>\n")

	if strings.TrimSpace(subject) != "" {
		b.WriteString(tgEscape(subject))
		b.WriteString("\n")
	}

	details := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		details = append(details, line)
	}
	if len(details) > 0 {
		b.WriteString("\n<b>详细信息</b>\n")
		for _, line := range details {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n时间：")
	b.WriteString(tgEscape(NowAlertTime()))
	return b.String()
}
