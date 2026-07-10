// Package notify delivers session alerts to the operator's configured
// destinations. Each destination gets a payload shaped for its platform: Slack
// attachments, Discord embeds, an HTML email, or a generic JSON webhook. No
// enabled destinations means every call is a silent no-op.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// Dispatcher fans one event out to every enabled destination. Safe for
// concurrent use.
type Dispatcher struct {
	store  *store.Store
	log    *slog.Logger
	client *http.Client
}

func NewDispatcher(st *store.Store, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{store: st, log: log, client: &http.Client{Timeout: 10 * time.Second}}
}

// alert is one thing worth telling the operator, in platform-neutral form.
type alert struct {
	Kind     string
	Protocol string
	Message  string
	Router   string
	Time     time.Time
}

func (a alert) title() string {
	switch a.Kind {
	case store.EventSessionDown:
		return "Session down: " + a.Protocol
	case store.EventSessionUp:
		return "Session recovered: " + a.Protocol
	case store.EventFlap:
		return "Session flapping: " + a.Protocol
	case store.EventLimitHit:
		return "Import limit reached: " + a.Protocol
	default:
		return "birdy alert"
	}
}

// severity drives the colour on every platform. good/warning/danger/info.
func (a alert) severity() string {
	switch a.Kind {
	case store.EventSessionDown, store.EventLimitHit:
		return "danger"
	case store.EventSessionUp:
		return "good"
	case store.EventFlap:
		return "warning"
	default:
		return "info"
	}
}

func (a alert) emoji() string {
	switch a.severity() {
	case "danger":
		return "\U0001F534"
	case "good":
		return "\U0001F7E2"
	case "warning":
		return "\U0001F7E1"
	default:
		return "\U0001F4E2"
	}
}

// hexColor / intColor give the same severity colour in the two forms Slack (hex
// string) and Discord (decimal int) each want.
func (a alert) hexColor() string {
	switch a.severity() {
	case "danger":
		return "#d64545"
	case "good":
		return "#2e9e6b"
	case "warning":
		return "#d9a441"
	default:
		return "#3b7dd8"
	}
}

func (a alert) intColor() int {
	switch a.severity() {
	case "danger":
		return 0xd64545
	case "good":
		return 0x2e9e6b
	case "warning":
		return 0xd9a441
	default:
		return 0x3b7dd8
	}
}

func (a alert) plainLine() string {
	line := a.emoji() + " " + a.title()
	if a.Router != "" {
		line += " (" + a.Router + ")"
	}
	return line + " — " + a.Message
}

// Notify delivers an event to every enabled destination, in the background so
// the poller is never blocked. A delivery failure is logged, not retried.
func (d *Dispatcher) Notify(kind, protocol, message string) {
	go func() {
		dests, err := d.store.EnabledAlertDestinations()
		if err != nil {
			d.log.Warn("could not load alert destinations", "error", err)
			return
		}
		if len(dests) == 0 {
			return
		}
		a := d.build(kind, protocol, message)
		for _, dest := range dests {
			if err := d.deliver(dest, a); err != nil {
				d.log.Warn("alert delivery failed", "destination", dest.Name, "type", dest.Type, "error", err)
			}
		}
	}()
}

// SendTest delivers a synthetic alert to one destination, inline, so the UI can
// report whether it worked.
func (d *Dispatcher) SendTest(dest store.Destination) error {
	a := d.build("test", "", "This is a test alert from birdy. If you can read this, alerts are wired up.")
	return d.deliver(dest, a)
}

func (d *Dispatcher) build(kind, protocol, message string) alert {
	router := ""
	if st, ok, _ := d.store.GetSettings(); ok {
		router = st.RouterLabel
	}
	return alert{Kind: kind, Protocol: protocol, Message: message, Router: router, Time: time.Now()}
}

func (d *Dispatcher) deliver(dest store.Destination, a alert) error {
	switch dest.Type {
	case store.AlertSlack:
		return d.postJSON(dest.URL, slackPayload(a))
	case store.AlertDiscord:
		return d.postJSON(dest.URL, discordPayload(a))
	case store.AlertEmail:
		return sendEmail(dest, a)
	default: // generic webhook
		return d.postJSON(dest.URL, webhookPayload(a))
	}
}

// postJSON marshals v and POSTs it, treating any non-2xx as an error.
func (d *Dispatcher) postJSON(url string, v any) error {
	body, err := json.Marshal(v)
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
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// ---- platform payloads ----

// webhookPayload carries text (Slack-compatible) and content (Discord-compatible)
// so a generic endpoint has something obvious to read, plus the structured
// fields for anything parsing the body.
func webhookPayload(a alert) map[string]any {
	return map[string]any{
		"text":     a.plainLine(),
		"content":  a.plainLine(),
		"event":    a.Kind,
		"protocol": a.Protocol,
		"message":  a.Message,
		"router":   a.Router,
		"severity": a.severity(),
		"time":     a.Time.UTC().Format(time.RFC3339),
	}
}

func slackPayload(a alert) map[string]any {
	fields := []map[string]any{{"title": "Event", "value": a.Kind, "short": true}}
	if a.Router != "" {
		fields = append(fields, map[string]any{"title": "Router", "value": a.Router, "short": true})
	}
	return map[string]any{
		"attachments": []map[string]any{{
			"fallback": a.plainLine(),
			"color":    a.hexColor(),
			"title":    a.emoji() + " " + a.title(),
			"text":     a.Message,
			"fields":   fields,
			"footer":   "birdy",
			"ts":       a.Time.Unix(),
		}},
	}
}

func discordPayload(a alert) map[string]any {
	fields := []map[string]any{{"name": "Event", "value": a.Kind, "inline": true}}
	if a.Router != "" {
		fields = append(fields, map[string]any{"name": "Router", "value": a.Router, "inline": true})
	}
	return map[string]any{
		"embeds": []map[string]any{{
			"title":       a.emoji() + " " + a.title(),
			"description": a.Message,
			"color":       a.intColor(),
			"fields":      fields,
			"footer":      map[string]any{"text": "birdy"},
			"timestamp":   a.Time.UTC().Format(time.RFC3339),
		}},
	}
}
