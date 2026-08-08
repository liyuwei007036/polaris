package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var cloudflareAPI = "https://api.cloudflare.com/client/v4"

type CloudflareRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

// CloudflareZoneInfo is the part of a zone that tells an operator whether the
// token really reaches the domain they typed.
type CloudflareZoneInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type CloudflareClient struct {
	token   string
	client  *http.Client
	baseURL string
}

func NewCloudflareClient(token string) (*CloudflareClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Cloudflare API token is required")
	}
	return &CloudflareClient{token: token, client: &http.Client{Timeout: 20 * time.Second}, baseURL: cloudflareAPI}, nil
}

// VerifyZone reads the zone itself, which is the cheapest call that proves the
// token is valid, has DNS permission on this zone and is reachable from here.
func (c *CloudflareClient) VerifyZone(ctx context.Context, zoneID string) (CloudflareZoneInfo, error) {
	var response struct {
		Result CloudflareZoneInfo `json:"result"`
	}
	if err := c.request(ctx, http.MethodGet, "/zones/"+zoneID, nil, &response); err != nil {
		return CloudflareZoneInfo{}, err
	}
	return response.Result, nil
}

func (c *CloudflareClient) ListRecords(ctx context.Context, zoneID string) ([]CloudflareRecord, error) {
	var response struct {
		Result []CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records", nil, &response); err != nil {
		return nil, err
	}
	return response.Result, nil
}

func (c *CloudflareClient) GetRecord(ctx context.Context, zoneID, recordID string) (CloudflareRecord, error) {
	var response struct {
		Result CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records/"+recordID, nil, &response); err != nil {
		return CloudflareRecord{}, err
	}
	return response.Result, nil
}

func (c *CloudflareClient) CreateRecord(ctx context.Context, zoneID string, record CloudflareRecord) (CloudflareRecord, error) {
	var response struct {
		Result CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", record, &response); err != nil {
		return CloudflareRecord{}, err
	}
	return response.Result, nil
}

// UpdateRecord patches rather than replaces: a record edited here may carry
// fields this console does not manage — a comment or tags set in Cloudflare's
// own UI — and a full PUT would silently drop them.
func (c *CloudflareClient) UpdateRecord(ctx context.Context, zoneID string, record CloudflareRecord) (CloudflareRecord, error) {
	if record.ID == "" {
		return CloudflareRecord{}, errors.New("Cloudflare record ID is required")
	}
	var response struct {
		Result CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodPatch, "/zones/"+zoneID+"/dns_records/"+record.ID, record, &response); err != nil {
		return CloudflareRecord{}, err
	}
	return response.Result, nil
}

func (c *CloudflareClient) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return c.request(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil)
}

func (c *CloudflareClient) request(ctx context.Context, method, path string, input any, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		// The operator has to be able to tell "cannot reach Cloudflare" apart
		// from "Cloudflare rejected us", so the reason is shown as-is.
		return userErrorf("无法连接 Cloudflare 接口：%v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result json.RawMessage `json:"result"`
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return userErrorf("Cloudflare 接口返回了无法解析的内容（HTTP %d）", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		if len(envelope.Errors) > 0 {
			return userErrorf("Cloudflare 接口返回错误：%s", envelope.Errors[0].Message)
		}
		return userErrorf("Cloudflare 接口返回 HTTP %d", response.StatusCode)
	}
	if output != nil {
		return json.Unmarshal(content, output)
	}
	return nil
}

func ValidateCloudflareProxy(recordType, listenerProtocol, transport string, port uint16, tls bool, proxied bool) error {
	if !proxied {
		return nil
	}
	if recordType != "A" && recordType != "AAAA" && recordType != "CNAME" {
		return errors.New("only A, AAAA and CNAME records can be proxied")
	}
	if listenerProtocol != "vless" || (transport != "ws" && transport != "grpc") {
		return errors.New("standard Cloudflare proxy only supports VLESS WebSocket and gRPC listeners")
	}
	httpPorts := map[uint16]bool{80: true, 8080: true, 8880: true, 2052: true, 2082: true, 2086: true, 2095: true}
	httpsPorts := map[uint16]bool{443: true, 2053: true, 2083: true, 2087: true, 2096: true, 8443: true}
	if transport == "grpc" {
		if !tls || port != 443 {
			return errors.New("Cloudflare gRPC requires TLS on port 443")
		}
	} else if tls {
		if !httpsPorts[port] {
			return errors.New("port is not supported by Cloudflare HTTPS proxy")
		}
	} else if !httpPorts[port] {
		return errors.New("port is not supported by Cloudflare HTTP proxy")
	}
	return nil
}
