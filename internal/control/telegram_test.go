package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTelegramClientSend(t *testing.T) {
	var (
		mu       sync.Mutex
		gotPath  string
		gotToken string
		gotMsg   telegramSendMessage
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotMsg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			mu.Unlock()
			return
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	defer server.Close()
	gotToken = "123456:ABC-DEF"

	client := NewTelegramClient()
	if err := client.Send(context.Background(), server.URL, gotToken, "-1001234567890", "hello telegram"); err != nil {
		t.Fatalf("send telegram: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/bot123456:ABC-DEF/sendMessage" {
		t.Errorf("unexpected request path: %s", gotPath)
	}
	if gotMsg.ChatID != "-1001234567890" {
		t.Errorf("unexpected chat id: %s", gotMsg.ChatID)
	}
	if gotMsg.Text != "hello telegram" {
		t.Errorf("unexpected text: %s", gotMsg.Text)
	}
	if !gotMsg.DisableWebPagePreview {
		t.Errorf("expected link previews to be disabled")
	}
}

func TestTelegramClientSendHTML(t *testing.T) {
	var got telegramSendMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	text := "<b>Polaris 管理通知</b>\n<code>Tokyo-01</code>"
	if err := NewTelegramClient().SendHTML(context.Background(), server.URL, "123456:ABC-DEF", "1", text); err != nil {
		t.Fatalf("send html telegram: %v", err)
	}
	if got.ParseMode != "HTML" {
		t.Errorf("expected HTML parse mode, got %q", got.ParseMode)
	}
	if got.Text != text {
		t.Errorf("unexpected text: %s", got.Text)
	}
}

func TestBuildTelegramAlertEscapesDynamicValues(t *testing.T) {
	text := buildTelegramAlert(severityInfo, "测试", "节点告警", "node<&>", []string{
		tgLine("字段", "<script>alert(1)</script>"),
	}, "点击 <a> 或联系管理员")

	if strings.Contains(text, "<script>") {
		t.Fatalf("dynamic value was not escaped: %s", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("expected escaped dynamic value, got: %s", text)
	}
	if !strings.Contains(text, "&lt;a&gt;") {
		t.Fatalf("expected escaped action text, got: %s", text)
	}
}

func TestTelegramClientRejectsErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
	}))
	defer server.Close()

	err := NewTelegramClient().Send(context.Background(), server.URL, "bad-token", "1", "hello")
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestAlertEngineTelegramOnlyChannel(t *testing.T) {
	ctx := context.Background()

	var (
		mu      sync.Mutex
		gotText []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg telegramSendMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotText = append(gotText, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		TelegramEnabled:     true,
		TelegramServer:      server.URL,
		TelegramBotToken:    "123456:ABC-DEF",
		TelegramChatID:      "-1009876543210",
		OfflineAlertEnabled: true,
		CooldownMinutes:     1,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.NotifyNodeOffline("node-tg", "Tokyo-TG")

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(gotText)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for telegram notification")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotText[0], "Tokyo-TG") {
		t.Errorf("expected node name in telegram text, got: %s", gotText[0])
	}
	if !strings.Contains(gotText[0], "节点已离线") {
		t.Errorf("expected alert title in telegram text, got: %s", gotText[0])
	}
	if !strings.Contains(gotText[0], "Polaris") || !strings.Contains(gotText[0], "<b>") {
		t.Errorf("expected bot-style HTML message, got: %s", gotText[0])
	}
	if !strings.Contains(gotText[0], "详细信息") || !strings.Contains(gotText[0], "建议操作") {
		t.Errorf("expected detailed bot message, got: %s", gotText[0])
	}
	if strings.Contains(gotText[0], "🤖") {
		t.Errorf("expected no decorative icons, got: %s", gotText[0])
	}
}
