// Package notify delivers session alerts to an operator's webhook. It is
// deliberately format-agnostic: the JSON payload carries both a "text" field
// (which Slack reads) and a "content" field (which Discord reads), plus the
// structured fields a generic consumer would want. No webhook configured means
// every call is a silent no-op.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// Webhook posts alerts to the URL stored in settings. Safe for concurrent use.
type Webhook struct {
	store  *store.Store
	log    *slog.Logger
	client *http.Client
}

func NewWebhook(st *store.Store, log *slog.Logger) *Webhook {
	if log == nil {
		log = slog.Default()
	}
	return &Webhook{store: st, log: log, client: &http.Client{Timeout: 10 * time.Second}}
}

// payload is what birdy POSTs. text/content cover Slack/Discord; the rest is for
// anything parsing the body itself.
type payload struct {
	Text     string `json:"text"`
	Content  string `json:"content"`
	Event    string `json:"event"`
	Protocol string `json:"protocol"`
	Message  string `json:"message"`
	Router   string `json:"router,omitempty"`
	Time     string `json:"time"`
}

// Notify sends an alert for one event, in the background so it never blocks the
// poller. A delivery failure is logged, not retried — a missed alert must not
// wedge the poll loop.
func (w *Webhook) Notify(kind, protocol, message string) {
	go w.send(kind, protocol, message)
}

func (w *Webhook) send(kind, protocol, message string) {
	settings, ok, err := w.store.GetSettings()
	if err != nil || !ok || settings.WebhookURL == "" {
		return
	}
	if err := w.deliver(settings.WebhookURL, kind, protocol, message, settings.RouterLabel, time.Now()); err != nil {
		w.log.Warn("webhook delivery failed", "url", settings.WebhookURL, "error", err)
	}
}

// deliver builds and POSTs the payload. Split out so a test can drive it against
// a local server without touching the store.
func (w *Webhook) deliver(url, kind, protocol, message, router string, now time.Time) error {
	line := prefix(kind) + " birdy"
	if router != "" {
		line += " [" + router + "]"
	}
	line += ": " + message

	body, err := json.Marshal(payload{
		Text: line, Content: line, Event: kind, Protocol: protocol,
		Message: message, Router: router, Time: now.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpError{resp.StatusCode}
	}
	return nil
}

// SendTest delivers a synthetic alert so an operator can confirm the webhook
// works from the settings page. Unlike Notify it runs inline and returns the
// error, so the UI can report success or failure.
func (w *Webhook) SendTest(url, router string) error {
	return w.deliver(url, "test", "", "test alert from birdy — your webhook is wired up", router, time.Now())
}

func prefix(kind string) string {
	switch kind {
	case store.EventSessionDown:
		return "\U0001F534" // red circle
	case store.EventSessionUp:
		return "\U0001F7E2" // green circle
	case store.EventFlap:
		return "\U0001F7E1" // yellow circle
	case store.EventLimitHit:
		return "⚠️" // warning sign
	default:
		return "\U0001F4E2" // loudspeaker
	}
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "webhook returned HTTP " + http.StatusText(e.code) }
