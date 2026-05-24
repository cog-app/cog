package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends OCM requests to remote servers.
type Client struct {
	HTTP    *http.Client
	BaseURL string // our base URL for constructing callback URLs
}

// NewClient creates an OCM client with SSRF protections: dials are gated by
// safeDialContext (blocks private/loopback/link-local/metadata IPs and pins to
// the resolved address), redirects are refused, and only HTTPS is permitted
// for remote hosts.
func NewClient(baseURL string) *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:       30 * time.Second,
			Transport:     safeTransport(),
			CheckRedirect: noRedirect,
		},
		BaseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

// Discover fetches the OCM provider info from a remote server.
func (c *Client) Discover(ctx context.Context, remoteHost string) (*OCMProviderResponse, error) {
	url := normalizeHost(remoteHost) + "/.well-known/ocm-provider"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", remoteHost, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover %s: status %d", remoteHost, resp.StatusCode)
	}

	var provider OCMProviderResponse
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return nil, fmt.Errorf("discover %s: decode: %w", remoteHost, err)
	}
	return &provider, nil
}

// SendShare sends a federated share to a remote server's OCM endpoint.
func (c *Client) SendShare(ctx context.Context, remoteHost string, share *OCMShare) (remoteID string, err error) {
	provider, err := c.Discover(ctx, remoteHost)
	if err != nil {
		return "", err
	}
	if !provider.Enabled {
		return "", fmt.Errorf("remote server %s has federation disabled", remoteHost)
	}

	endpoint := provider.EndPoint
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = normalizeHost(remoteHost) + endpoint
	}

	body, err := json.Marshal(share)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/shares", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("send share to %s: %w", remoteHost, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("send share to %s: status %d: %s", remoteHost, resp.StatusCode, respBody)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response from %s: %w", remoteHost, err)
	}
	return result.ID, nil
}

// Notify sends a state change notification to a remote server.
func (c *Client) Notify(ctx context.Context, remoteHost string, notif OCMNotification) error {
	provider, err := c.Discover(ctx, remoteHost)
	if err != nil {
		return err
	}

	endpoint := provider.EndPoint
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = normalizeHost(remoteHost) + endpoint
	}

	body, err := json.Marshal(notif)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/notifications", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notify %s: %w", remoteHost, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notify %s: status %d", remoteHost, resp.StatusCode)
	}
	return nil
}

// normalizeHost prepends https:// when missing. An explicit http:// is honored
// only in allowPrivate (test) mode; in production normalizeHost upgrades it to
// https:// to defeat downgrade attacks.
func normalizeHost(host string) string {
	host = strings.TrimSuffix(host, "/")
	if strings.HasPrefix(host, "https://") {
		return host
	}
	if strings.HasPrefix(host, "http://") {
		if allowPrivate {
			return host
		}
		return "https://" + strings.TrimPrefix(host, "http://")
	}
	return "https://" + host
}
