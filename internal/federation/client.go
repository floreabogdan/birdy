// Package federation contains the deliberately small, read-only protocol used
// when one Birdy panel observes another Birdy instance.
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxDashboardBytes = 1 << 20

var defaultHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTPClient
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type Event struct {
	ID       int64     `json:"id"`
	Ts       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Protocol string    `json:"protocol"`
	Actor    string    `json:"actor,omitempty"`
	Message  string    `json:"message"`
}

// Check performs the same bounded read as FetchDashboard but reports latency
// separately, which lets the fleet UI distinguish a slow target from a down
// target without parsing dashboard details twice.
func (c Client) Check(ctx context.Context) (time.Duration, error) {
	started := time.Now()
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" || c.Token == "" {
		return time.Since(started), fmt.Errorf("remote Birdy target is missing its URL or token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/dashboard", nil)
	if err != nil {
		return time.Since(started), err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "birdy-federation-health")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return time.Since(started), fmt.Errorf("remote health request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Since(started), fmt.Errorf("remote Birdy returned HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	return time.Since(started), err
}

func (c Client) FetchDashboard(ctx context.Context) ([]byte, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" || c.Token == "" {
		return nil, fmt.Errorf("remote Birdy target is missing its URL or token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/dashboard", nil)
	if err != nil {
		return nil, fmt.Errorf("build remote dashboard request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "birdy-federation")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote dashboard request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote Birdy returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDashboardBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read remote dashboard: %w", err)
	}
	if len(body) > maxDashboardBytes {
		return nil, fmt.Errorf("remote dashboard exceeds 1 MiB")
	}
	return body, nil
}

// FetchEvents reads only the small, read-only timeline projection used by the
// fleet activity view. The server remains responsible for the result limit.
func (c Client) FetchEvents(ctx context.Context, limit int) ([]Event, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" || c.Token == "" {
		return nil, fmt.Errorf("remote Birdy target is missing its URL or token")
	}
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("remote event limit must be between 1 and 50")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/events?limit=%d", base, limit), nil)
	if err != nil {
		return nil, fmt.Errorf("build remote events request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "birdy-federation")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote events request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote Birdy returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Events []Event `json:"events"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<10))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode remote events: %w", err)
	}
	return payload.Events, nil
}
