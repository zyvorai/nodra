// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package relaybridge

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client publishes mapped events into Relay (direct) or relay-pubsub (gateway).
type Client struct {
	RelayBase    string
	RelayToken   string
	GatewayBase  string
	GatewayToken string
	Project      string
	TLSInsecure  bool
	HTTP         *http.Client
}

// PublishResult describes where the event was accepted.
type PublishResult struct {
	Path    string `json:"path"`
	EventID string `json:"event_id,omitempty"`
	Raw     string `json:"raw,omitempty"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.TLSInsecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // lab self-signed
	}
	c.HTTP = &http.Client{Timeout: 15 * time.Second, Transport: tr}
	return c.HTTP
}

// Publish sends a Relay Accept event via gateway when configured, else direct.
func (c *Client) Publish(ev RelayEvent) (PublishResult, error) {
	if c.GatewayBase != "" {
		return c.publishViaGateway(ev)
	}
	return c.publishDirect(ev)
}

func (c *Client) publishDirect(ev RelayEvent) (PublishResult, error) {
	if strings.TrimSpace(c.RelayBase) == "" {
		return PublishResult{}, fmt.Errorf("RELAY_BASE_URL is required when GATEWAY_BASE_URL is empty")
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return PublishResult{}, err
	}
	url := strings.TrimRight(c.RelayBase, "/") + "/v1/events"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return PublishResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.RelayToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.RelayToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return PublishResult{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode/100 != 2 {
		return PublishResult{}, fmt.Errorf("relay events %s: %s", resp.Status, string(b))
	}
	var out struct {
		Event struct {
			ID string `json:"id"`
		} `json:"event"`
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &out)
	id := out.Event.ID
	if id == "" {
		id = out.ID
	}
	return PublishResult{Path: "relay", EventID: id, Raw: string(b)}, nil
}

func (c *Client) publishViaGateway(ev RelayEvent) (PublishResult, error) {
	payload, _ := json.Marshal(ev.Data)
	attrs := map[string]string{
		"severity":        ev.Severity,
		"source":          ev.Source,
		"idempotency_key": ev.IdempotencyKey,
	}
	body := map[string]any{
		"messages": []map[string]any{{
			"data":       base64.StdEncoding.EncodeToString(payload),
			"attributes": attrs,
		}},
	}
	raw, _ := json.Marshal(body)
	project := c.Project
	if project == "" {
		project = "fasal-onprem"
	}
	base := strings.TrimRight(c.GatewayBase, "/")
	url := base + "/v1/projects/" + project + "/topics/" + ev.Type + ":publish"
	res, err := c.doGatewayPublish(url, raw)
	if err == nil {
		return res, nil
	}
	if strings.Contains(err.Error(), "404") {
		if cerr := c.ensureGatewayTopic(project, ev.Type); cerr != nil {
			return PublishResult{}, fmt.Errorf("%w (ensure topic: %v)", err, cerr)
		}
		return c.doGatewayPublish(url, raw)
	}
	return PublishResult{}, err
}

func (c *Client) doGatewayPublish(url string, raw []byte) (PublishResult, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return PublishResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.GatewayToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return PublishResult{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode/100 != 2 {
		return PublishResult{}, fmt.Errorf("gateway publish %s: %s", resp.Status, string(b))
	}
	return PublishResult{Path: "gateway", Raw: string(b)}, nil
}

func (c *Client) ensureGatewayTopic(project, eventType string) error {
	base := strings.TrimRight(c.GatewayBase, "/")
	url := base + "/v1/projects/" + project + "/topics/" + eventType
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.GatewayToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("create topic %s: %s", resp.Status, string(b))
}
