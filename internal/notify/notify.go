// Package notify delivers session alerts to the operator's configured
// destinations. Each destination gets a payload shaped for its platform: Slack
// attachments, Discord embeds, an HTML email, or a generic JSON webhook. No
// enabled destinations means every call is a silent no-op.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// throttled are the storm-prone session kinds a cooldown applies to. Config
// events (apply/revert) are operator-initiated and infrequent, so they always
// go through.
var throttled = map[string]bool{
	store.EventSessionDown: true, store.EventSessionUp: true,
	store.EventFlap: true, store.EventLimitHit: true, store.EventPrefixDrop: true,
}

// Dispatcher fans one event out to every enabled destination. Safe for
// concurrent use.
type Dispatcher struct {
	store    *store.Store
	log      *slog.Logger
	client   *http.Client
	cooldown time.Duration
	// smtpTimeout bounds one alert email; a field so tests can shorten it.
	smtpTimeout time.Duration
	now         func() time.Time // the throttle's clock; tests substitute one

	mu     sync.Mutex
	last   map[string]time.Time    // (kind|protocol) -> last delivery, for throttling
	told   map[string]string       // protocol -> kind of the last throttled alert delivered
	held   map[string]notification // protocol -> newest event the cooldown held back
	outbox map[int64]chan delivery // destination ID -> its own delivery queue

	queueOnce sync.Once
	queue     chan notification // FIFO into the single routing worker
}

// queueSize bounds the routing queue and each destination's queue.
const queueSize = 256

// heldCheckEvery is how often the routing worker looks for held-back events
// whose cooldown has run out; the cooldown itself, if that is shorter.
const heldCheckEvery = 10 * time.Second

type notification struct{ kind, protocol, message string }

// delivery is one alert bound for one destination.
type delivery struct {
	dest  store.Destination
	alert alert
}

// NewDispatcher builds a dispatcher. cooldown suppresses a repeat of the same
// session event for a protocol within the window; 0 disables throttling.
func NewDispatcher(st *store.Store, log *slog.Logger, cooldown time.Duration) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store: st, log: log, cooldown: cooldown, smtpTimeout: smtpTimeout, now: time.Now,
		client: &http.Client{Timeout: 10 * time.Second},
		last:   map[string]time.Time{},
		told:   map[string]string{},
		held:   map[string]notification{},
		outbox: map[int64]chan delivery{},
	}
}

// throttle reports whether an event should be held back as a repeat, and
// records this delivery when it is not. Only the storm-prone session kinds are
// subject to it. A held-back event is kept as its protocol's newest news, for
// takeHeld to deliver once the window closes.
func (d *Dispatcher) throttle(n notification) bool {
	if d.cooldown <= 0 || !throttled[n.kind] {
		return false
	}
	key := n.kind + "|" + n.protocol
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if last, ok := d.last[key]; ok && now.Sub(last) < d.cooldown {
		d.held[n.protocol] = n
		return true
	}
	d.last[key] = now
	d.told[n.protocol] = n.kind
	delete(d.held, n.protocol) // superseded: this is now the newest thing said
	return false
}

// takeHeld returns, and records as delivered, the held-back events whose window
// has closed and that differ from the last alert the operator got for that
// protocol. The throttle is keyed per kind, so without this, down, flap, down
// (held) leaves the operator's last word "flapping" while the session sits down,
// and no further transition comes along to correct it. A held repeat of what
// was last said (flap, flap) is dropped, as before.
func (d *Dispatcher) takeHeld() []notification {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	var due []notification
	for protocol, n := range d.held {
		key := n.kind + "|" + protocol
		if now.Sub(d.last[key]) < d.cooldown {
			continue
		}
		delete(d.held, protocol)
		if d.told[protocol] == n.kind {
			continue
		}
		d.last[key] = now
		d.told[protocol] = n.kind
		n.message += " (held back by the alert cooldown)"
		due = append(due, n)
	}
	sort.Slice(due, func(i, j int) bool { return due[i].protocol < due[j].protocol })
	return due
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
	case store.EventPrefixDrop:
		return "Prefix count dropped: " + a.Protocol
	case store.EventBirdUnreach:
		return "BIRD is unreachable"
	case store.EventBirdReachable:
		return "BIRD is reachable again"
	case store.EventConfigApply:
		return "Config applied"
	case store.EventConfigRevert:
		return "Config reverted"
	case store.EventConfigDrift:
		return "Config changed outside birdy"
	case store.EventIRRRefresh:
		return "Prefix set refreshed from IRR"
	default:
		return "birdy alert"
	}
}

// severity drives the colour on every platform. good/warning/danger/info.
func (a alert) severity() string {
	switch a.Kind {
	case store.EventSessionDown, store.EventLimitHit, store.EventBirdUnreach, store.EventPrefixDrop:
		return "danger"
	case store.EventSessionUp, store.EventBirdReachable:
		return "good"
	case store.EventFlap, store.EventConfigDrift, store.EventConfigRevert:
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

// Notify queues an event for delivery to every enabled destination. A single
// background worker takes events in order, applies the throttle and each
// destination's filter, and hands the alert to that destination's own queue.
// Each destination delivers its queue in order (a session_up never overtakes the
// session_down before it) and one request at a time, so a flap storm never fans
// out into an unbounded burst of concurrent requests — and a destination that is
// down or slow delays only itself, never the others. Every queue is bounded; if
// one fills, the overflow is logged and dropped rather than blocking the poller.
// A delivery failure is logged, not retried.
func (d *Dispatcher) Notify(kind, protocol, message string) {
	d.queueOnce.Do(func() {
		d.queue = make(chan notification, queueSize)
		go d.deliverLoop()
	})
	select {
	case d.queue <- notification{kind: kind, protocol: protocol, message: message}:
	default:
		d.log.Warn("alert queue full; dropping notification", "kind", kind, "protocol", protocol)
	}
}

func (d *Dispatcher) deliverLoop() {
	var heldCheck <-chan time.Time // nil, so never ready, without a cooldown
	if d.cooldown > 0 {
		t := time.NewTicker(min(d.cooldown, heldCheckEvery))
		defer t.Stop()
		heldCheck = t.C
	}
	for {
		select {
		case n := <-d.queue:
			if !d.throttle(n) {
				d.dispatch(n)
			}
		case <-heldCheck:
			for _, n := range d.takeHeld() {
				d.dispatch(n)
			}
		}
	}
}

func (d *Dispatcher) dispatch(n notification) {
	for _, j := range d.route(n.kind, n.protocol, n.message) {
		d.handOff(j)
	}
}

// route builds the alert for an event once and pairs it with every enabled
// destination that wants this kind.
func (d *Dispatcher) route(kind, protocol, message string) []delivery {
	dests, err := d.store.EnabledAlertDestinations()
	if err != nil {
		d.log.Warn("could not load alert destinations", "error", err)
		return nil
	}
	if len(dests) == 0 {
		return nil
	}
	// A recovery follows its outage's subscription: if you asked to hear that
	// BIRD went unreachable, you hear when it comes back, without a second box.
	filterKind := kind
	if kind == store.EventBirdReachable {
		filterKind = store.EventBirdUnreach
	}

	a := d.build(kind, protocol, message)
	var out []delivery
	for _, dest := range dests {
		if dest.Wants(filterKind) {
			out = append(out, delivery{dest: dest, alert: a})
		}
	}
	return out
}

// handOff puts a delivery on its destination's queue, starting that
// destination's worker if it has none running.
func (d *Dispatcher) handOff(j delivery) {
	d.mu.Lock()
	q, ok := d.outbox[j.dest.ID]
	if !ok {
		q = make(chan delivery, queueSize)
		d.outbox[j.dest.ID] = q
		go d.destLoop(j.dest.ID, q)
	}
	queued := true
	select {
	case q <- j:
	default:
		queued = false
	}
	d.mu.Unlock()
	if !queued {
		d.log.Warn("alert queue full; dropping notification",
			"destination", j.dest.Name, "kind", j.alert.Kind, "protocol", j.alert.Protocol)
	}
}

// destLoop delivers one destination's queue in order, and exits once it is
// empty so a deleted destination leaves no goroutine behind. The emptiness check
// is made under the lock handOff holds while queueing, so a delivery can never
// land on a queue whose worker has already gone.
func (d *Dispatcher) destLoop(id int64, q chan delivery) {
	for {
		select {
		case j := <-q:
			d.deliverOne(j)
		default:
			d.mu.Lock()
			if len(q) == 0 {
				delete(d.outbox, id)
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()
		}
	}
}

func (d *Dispatcher) deliverOne(j delivery) {
	if err := d.deliver(j.dest, j.alert); err != nil {
		d.log.Warn("alert delivery failed", "destination", j.dest.Name, "type", j.dest.Type, "error", err)
	}
}

// SendTest delivers a synthetic alert to one destination, inline, so the UI can
// report whether it worked.
func (d *Dispatcher) SendTest(dest store.Destination) error {
	a := d.build("test", "", "This is a test alert from birdy. If you can read this, alerts are wired up.")
	return d.deliver(dest, a)
}

// MailConfig emails a copy of the applied config to every enabled EMAIL
// destination — an off-box record of what is running, landing in an inbox that
// survives the router's disk. It is background and best-effort; a chat webhook
// is no place for a config dump, so only email destinations get it. The config
// passed in must already be password-masked.
func (d *Dispatcher) MailConfig(maskedConfig string) {
	go func() {
		dests, err := d.store.EnabledAlertDestinations()
		if err != nil {
			return
		}
		router := ""
		if st, ok, _ := d.store.GetSettings(); ok {
			router = st.RouterLabel
		}
		for _, dest := range dests {
			if !dest.IsEmail() {
				continue
			}
			if err := mailConfig(dest, router, maskedConfig); err != nil {
				d.log.Warn("config-backup email failed", "destination", dest.Name, "error", err)
			}
		}
	}()
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
		return sendEmail(dest, a, d.smtpTimeout)
	default: // generic webhook
		return d.postJSON(dest.URL, webhookPayload(a))
	}
}

// postJSON marshals v and POSTs it, treating any non-2xx as an error. Its errors
// name only the scheme and host: a Slack or Discord webhook URL is itself the
// credential, and these errors are logged and shown in the browser.
func (d *Dispatcher) postJSON(rawURL string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST %s: %w", redactURL(rawURL), withoutURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", redactURL(rawURL), withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", redactURL(rawURL), resp.StatusCode)
	}
	return nil
}

// redactURL is a webhook URL cut down to scheme and host, enough to tell the
// operator which service failed without the path or userinfo that carry the
// secret.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "webhook"
	}
	return u.Scheme + "://" + u.Host
}

// withoutURL peels off the *url.Error the HTTP client wraps every failure in;
// its message quotes the full request URL. What is left (a dial, TLS or timeout
// error) names at most the host.
func withoutURL(err error) error {
	var ue *url.Error
	for errors.As(err, &ue) {
		err = ue.Err
	}
	return err
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
