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
	BarkTitle string
	BarkBody  string
	BarkGroup string
	BarkSound string
	BarkURL   string
	Telegram  string
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
func buildTelegramAlert(category, headline, subject string, lines []string, action string) string {
	var b strings.Builder
	b.WriteString("<b>Polaris Bot 告警</b>\n")
	b.WriteString("<b>")
	b.WriteString(tgEscape(category))
	b.WriteString(" / ")
	b.WriteString(tgEscape(headline))
	b.WriteString("</b>\n")

	if strings.TrimSpace(subject) != "" {
		b.WriteString("对象：")
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

	if action != "" {
		b.WriteString("\n<b>处理建议</b>\n")
		b.WriteString(tgEscape(action))
		b.WriteString("\n")
	}

	b.WriteString("\n时间：")
	b.WriteString(tgEscape(NowAlertTime()))
	return b.String()
}
