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
)

type BarkMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Group string `json:"group,omitempty"`
	Sound string `json:"sound,omitempty"`
	Level string `json:"level,omitempty"` // active, critical, passive, timeSensitive
	Icon  string `json:"icon,omitempty"`
	URL   string `json:"url,omitempty"`
}

type BarkClient struct {
	httpClient *http.Client
}

func NewBarkClient() *BarkClient {
	return &BarkClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send sends a push message to the configured Bark server and device key.
func (c *BarkClient) Send(ctx context.Context, server, deviceKey string, msg BarkMessage) error {
	server = strings.TrimSpace(server)
	if server == "" {
		server = "https://api.day.app"
	}
	server = strings.TrimRight(server, "/")

	deviceKey = strings.TrimSpace(deviceKey)
	if deviceKey == "" {
		return errors.New("bark device key is not configured")
	}

	// Normalize target URL.
	// If server already contains deviceKey (e.g. https://api.day.app/abc/ or https://bark.com/push), handle both.
	var targetURL string
	if strings.Contains(server, deviceKey) {
		targetURL = server
	} else {
		targetURL = server + "/" + deviceKey
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal bark message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create bark request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send bark request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("bark push failed with HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("bark server error code %d: %s", result.Code, result.Message)
	}

	return nil
}
