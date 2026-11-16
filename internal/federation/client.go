// Package federation contains the deliberately small, read-only protocol used
// when one Birdy panel observes another Birdy instance.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

const maxDashboardBytes = 1 << 20

// defaultHTTPClient is what every remote read goes through unless a caller
// supplies its own client: bounded, never following a redirect, and refusing at
// connect time any address BlockedAddr names.
var defaultHTTPClient = newHTTPClient(BlockedAddr)

// BlockedAddr reports whether an address can only be an SSRF pivot rather than
// a reachable remote Birdy: loopback, link-local (including the cloud metadata
// range 169.254/16 and fe80::/10), unspecified, or multicast — in their
// IPv4-mapped IPv6 forms too. Private and ULA ranges are allowed on purpose:
// routers commonly observe each other over management networks.
//
// It is the single predicate behind both checks birdy makes: the web layer
// applies it to the URL an operator saves, for a friendly error, and the
// transport applies it to the address each connection is actually made to.
func BlockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast()
}

// errBlockedAddr is what a refused connection attempt fails with. It reaches
// the operator in the instance's "last error", so it says what happened in the
// same words as the save-time check.
var errBlockedAddr = errors.New("that address cannot be a remote Birdy target")

// newHTTPClient builds the federation client around a dial-time address check.
//
// Checking the URL when an instance is saved is not enough on its own. A name
// can resolve to a public address that day and to 127.0.0.1 or 169.254.169.254
// the next (or between the check and the request — DNS rebinding), and a name
// that did not resolve at all when it was saved was let through. So the check
// that counts runs in the dialer's Control hook, which the kernel-bound socket
// passes through after name resolution and before connect: the address it sees
// is the literal IP this connection is going to, for every attempt, including
// each address of a multi-record name. The transport sets no proxy, so that
// address is always the remote itself.
//
// blocked is BlockedAddr in production. It is a parameter only so tests, whose
// httptest servers listen on loopback, can build the same client with that one
// range let through.
func newHTTPClient(blocked func(netip.Addr) bool) *http.Client {
	// Otherwise a zero Dialer, which is what the transport used before it had
	// a DialContext: the client's own timeouts bound the attempt.
	dialer := &net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				// Not a literal IP and port: nothing birdy dials looks like that,
				// so refuse rather than connect to something unchecked.
				return fmt.Errorf("refusing to connect to %q: %w", address, errBlockedAddr)
			}
			if blocked(ap.Addr()) {
				return errBlockedAddr
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 4 * time.Second,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
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
