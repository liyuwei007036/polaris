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
	DeviceKey string `json:"device_key,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Group     string `json:"group,omitempty"`
	Sound     string `json:"sound,omitempty"`
	Level     string `json:"level,omitempty"` // active, timeSensitive
	Icon      string `json:"icon,omitempty"`
	URL       string `json:"url,omitempty"`
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

	// Always populate device_key in payload for Bark V2 JSON specification
	msg.DeviceKey = deviceKey

	// Ensure sound has .caf suffix for direct iOS APNs audio bundle lookup.
	// Without .caf, iOS APNs cannot locate the sound file and defaults to Tri-tone.
	sound := strings.TrimSpace(msg.Sound)
	if sound == "" {
		sound = "minuet.caf"
	} else if !strings.HasSuffix(sound, ".caf") {
		sound = sound + ".caf"
	}
	msg.Sound = sound

	// Interruption level in iOS:
	// - "passive" deliberately silences notification (no sound, no screen wake).
	// - "critical" requires special Apple Critical Alerts entitlement and user manual switch in iOS Settings.
	// Standardize to "active" or "timeSensitive" so notifications ALWAYS play their custom sound!
	if msg.Level == "" || msg.Level == "passive" || msg.Level == "critical" {
		msg.Level = "active"
	}

	// Normalize target URL.
	// Bark V2 API standard endpoint is POST /push with device_key in JSON body.
	var targetURL string
	if strings.HasSuffix(server, "/push") {
		targetURL = server
	} else if strings.Contains(server, deviceKey) {
		targetURL = server
	} else {
		targetURL = server + "/push"
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

	// If /push returned 404 (e.g. legacy/minimalist bark server), fallback to /:device_key
	if resp.StatusCode == http.StatusNotFound && targetURL == server+"/push" {
		fallbackURL := server + "/" + deviceKey
		req2, err2 := http.NewRequestWithContext(ctx, http.MethodPost, fallbackURL, bytes.NewReader(payload))
		if err2 == nil {
			req2.Header.Set("Content-Type", "application/json; charset=utf-8")
			if resp2, err3 := c.httpClient.Do(req2); err3 == nil {
				defer resp2.Body.Close()
				resp = resp2
			}
		}
	}

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
