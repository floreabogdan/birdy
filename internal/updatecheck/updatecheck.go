// Package updatecheck compares the running build with Birdy's stable release
// or development branch using the public GitHub API.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ChannelStable      = "stable"
	ChannelDevelopment = "development"
	defaultAPIBase     = "https://api.github.com/repos/floreabogdan/birdy"
	maxResponseBytes   = 1 << 20
)

type Result struct {
	Channel        string
	CurrentVersion string
	CurrentCommit  string
	LatestVersion  string
	LatestCommit   string
	URL            string
	Available      bool
	CheckedAt      time.Time
}

type cachedResult struct {
	result Result
	err    error
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
	TTL     time.Duration

	mu    sync.Mutex
	cache map[string]cachedResult
}

func New() *Client {
	return &Client{
		BaseURL: defaultAPIBase,
		HTTP:    &http.Client{Timeout: 8 * time.Second},
		TTL:     15 * time.Minute,
		cache:   make(map[string]cachedResult),
	}
}

func ValidChannel(channel string) bool {
	return channel == ChannelStable || channel == ChannelDevelopment
}

func (c *Client) Check(ctx context.Context, channel, currentVersion, currentCommit string) (Result, error) {
	if !ValidChannel(channel) {
		return Result{}, fmt.Errorf("update check: unsupported channel %q", channel)
	}
	key := channel + "\x00" + currentVersion + "\x00" + currentCommit
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		ttl := c.ttl()
		if cached.err != nil {
			ttl = c.negativeTTL()
		}
		if time.Since(cached.result.CheckedAt) < ttl {
			c.mu.Unlock()
			return cached.result, cached.err
		}
	}
	c.mu.Unlock()

	result, err := c.check(ctx, channel, currentVersion, currentCommit)
	// Cache both outcomes, but a failure only for a short negative TTL: long
	// enough that a slow or rate-limited GitHub is not re-hit on every page
	// render (the check runs inline in the request), short enough that a
	// transient outage clears on its own without pinning the full TTL.
	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]cachedResult)
	}
	c.cache[key] = cachedResult{result: result, err: err}
	c.mu.Unlock()
	return result, err
}

func (c *Client) ttl() time.Duration {
	if c.TTL <= 0 {
		return 15 * time.Minute
	}
	return c.TTL
}

// negativeTTL bounds how long a failed check is cached — capped well under the
// success TTL so an outage self-heals quickly, but long enough to stop a burst
// of renders from hammering a down or rate-limited GitHub.
func (c *Client) negativeTTL() time.Duration {
	if t := c.ttl(); t < 2*time.Minute {
		return t
	}
	return 2 * time.Minute
}

func (c *Client) check(ctx context.Context, channel, currentVersion, currentCommit string) (Result, error) {
	result := Result{
		Channel: channel, CurrentVersion: currentVersion,
		CurrentCommit: currentCommit, CheckedAt: time.Now(),
	}
	if channel == ChannelStable {
		var release struct {
			TagName string `json:"tag_name"`
			HTMLURL string `json:"html_url"`
		}
		if err := c.getJSON(ctx, "/releases/latest", &release); err != nil {
			return result, err
		}
		result.LatestVersion = strings.TrimPrefix(release.TagName, "v")
		result.URL = release.HTMLURL
		result.Available = stableAvailable(currentVersion, result.LatestVersion)
		return result, nil
	}

	var commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.getJSON(ctx, "/commits/main", &commit); err != nil {
		return result, err
	}
	result.LatestCommit = commit.SHA
	result.URL = commit.HTMLURL
	result.Available = currentCommit == "" || currentCommit == "unknown" ||
		!strings.HasPrefix(commit.SHA, currentCommit)
	return result, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = defaultAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "birdy-update-check")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("update check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update check: GitHub returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("update check: read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("update check: response exceeds 1 MiB")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("update check: decode response: %w", err)
	}
	return nil
}

func stableAvailable(current, latest string) bool {
	cur, okCur := semverTuple(current)
	next, okNext := semverTuple(latest)
	if !okCur || !okNext {
		return strings.TrimPrefix(current, "v") != strings.TrimPrefix(latest, "v")
	}
	for i := range cur {
		if next[i] != cur[i] {
			return next[i] > cur[i]
		}
	}
	// Identical release numbers: a pre-release build (e.g. 0.4.2-dev) sits just
	// before its own stable tag, so offer that tag once it ships. A -dev build
	// whose number is already ahead of the latest stable (0.4.2-dev vs 0.4.1)
	// returned false from the comparison above — it is ahead, not behind.
	return isPrerelease(current) && !isPrerelease(latest)
}

// isPrerelease reports whether a version string carries a pre-release suffix,
// such as the "-dev" birdy stamps on builds past the last release tag.
func isPrerelease(version string) bool {
	return strings.Contains(strings.TrimSpace(version), "-")
}

func semverTuple(version string) ([3]int, bool) {
	var out [3]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	version = strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
