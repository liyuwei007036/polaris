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

func (c *CloudflareClient) ListRecords(ctx context.Context, zoneID string) ([]CloudflareRecord, error) {
	var response struct {
		Result []CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records", nil, &response); err != nil {
		return nil, err
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

func (c *CloudflareClient) UpdateRecord(ctx context.Context, zoneID string, record CloudflareRecord) (CloudflareRecord, error) {
	if record.ID == "" {
		return CloudflareRecord{}, errors.New("Cloudflare record ID is required")
	}
	var response struct {
		Result CloudflareRecord `json:"result"`
	}
	if err := c.request(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+record.ID, record, &response); err != nil {
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
		return err
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
		return fmt.Errorf("decode Cloudflare API response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		if len(envelope.Errors) > 0 {
			return fmt.Errorf("Cloudflare API: %s", envelope.Errors[0].Message)
		}
		return fmt.Errorf("Cloudflare API returned HTTP %d", response.StatusCode)
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
	if listenerProtocol != "http" && transport != "ws" && transport != "httpupgrade" {
		return errors.New("standard Cloudflare proxy only supports HTTP/HTTPS or WebSocket listeners")
	}
	httpPorts := map[uint16]bool{80: true, 8080: true, 8880: true, 2052: true, 2082: true, 2086: true, 2095: true}
	httpsPorts := map[uint16]bool{443: true, 2053: true, 2083: true, 2087: true, 2096: true, 8443: true}
	if tls {
		if !httpsPorts[port] {
			return errors.New("port is not supported by Cloudflare HTTPS proxy")
		}
	} else if !httpPorts[port] {
		return errors.New("port is not supported by Cloudflare HTTP proxy")
	}
	return nil
}
