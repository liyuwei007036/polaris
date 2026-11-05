package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	telegramDefaultAPIBase = "https://api.telegram.org"
	telegramMaxTextLength  = 4000
)

type TelegramClient struct {
	httpClient *http.Client
}

func NewTelegramClient() *TelegramClient {
	return &TelegramClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type telegramSendMessage struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
	DisableNotification   bool   `json:"disable_notification,omitempty"`
	MessageThreadID       int    `json:"message_thread_id,omitempty"`
}

// Send posts a plain-text message through the Telegram Bot API.
// server is the Bot API base (defaults to https://api.telegram.org) and may point
// at a self-hosted Bot API instance or a reverse proxy.
func (c *TelegramClient) Send(ctx context.Context, server, botToken, chatID, text string) error {
	return c.send(ctx, server, botToken, chatID, text, "")
}

// SendHTML posts an HTML-formatted bot message through the Telegram Bot API.
// Dynamic values must be escaped by the caller; see tgLine/tgCodeLine helpers.
func (c *TelegramClient) SendHTML(ctx context.Context, server, botToken, chatID, text string) error {
	return c.send(ctx, server, botToken, chatID, text, "HTML")
}

func (c *TelegramClient) send(ctx context.Context, server, botToken, chatID, text, parseMode string) error {
	botToken = strings.TrimSpace(botToken)
	chatID = strings.TrimSpace(chatID)
	if botToken == "" {
		return errors.New("telegram bot token is not configured")
	}
	if chatID == "" {
		return errors.New("telegram chat id is not configured")
	}

	server = strings.TrimSpace(server)
	if server == "" {
		server = telegramDefaultAPIBase
	}
	server = strings.TrimRight(server, "/")

	targetURL := server
	switch {
	case strings.HasSuffix(server, "/sendMessage"):
		// Full endpoint already provided.
	case strings.HasSuffix(server, "/bot"+botToken):
		targetURL = server + "/sendMessage"
	default:
		targetURL = server + "/bot" + botToken + "/sendMessage"
	}

	text = strings.TrimSpace(text)
	if text == "" {
		text = "Polaris 通知"
	}
	if utf8.RuneCountInString(text) > telegramMaxTextLength {
		runes := []rune(text)
		text = string(runes[:telegramMaxTextLength-1]) + "…"
	}

	payload, err := json.Marshal(telegramSendMessage{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             parseMode,
		DisableWebPagePreview: true,
	})
	if err != nil {
		return fmt.Errorf("marshal telegram message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &result)

	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(result.Description)
		if detail == "" {
			detail = strings.TrimSpace(string(body))
		}
		if hint := telegramErrorHint(detail); hint != "" {
			return errors.New(hint)
		}
		return fmt.Errorf("telegram push failed with HTTP %d: %s", resp.StatusCode, detail)
	}
	if !result.OK {
		if hint := telegramErrorHint(result.Description); hint != "" {
			return errors.New(hint)
		}
		return fmt.Errorf("telegram server rejected message: %s", result.Description)
	}

	return nil
}

// telegramErrorHint turns the most common Bot API failures into actionable
// messages instead of returning only Telegram's English description.
func telegramErrorHint(description string) string {
	detail := strings.ToLower(strings.TrimSpace(description))
	switch {
	case strings.Contains(detail, "chat not found"):
		return "Telegram 找不到该 Chat ID：请确认机器人已加入目标群组或频道；超级群请使用 -100 开头的 ID；私聊请先向机器人发送 /start"
	case strings.Contains(detail, "group chat was upgraded to a supergroup chat"):
		return "Telegram 群组已升级为超级群：请改用新的 -100 开头 Chat ID"
	case strings.Contains(detail, "bot was blocked by the user"):
		return "Telegram 用户已屏蔽该机器人：请先取消屏蔽，再发送 /start"
	case strings.Contains(detail, "not enough rights"):
		return "Telegram 机器人权限不足：请在群组或频道中授予机器人发言或管理员权限"
	case strings.Contains(detail, "user not found"):
		return "Telegram Chat ID 无效：请确认填写的是数字 ID，而不是用户名"
	case strings.Contains(detail, "chat_id is empty"):
		return "Telegram Chat ID 为空：请填写目标私聊、群组或频道的 Chat ID"
	default:
		return ""
	}
}
