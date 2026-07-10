package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Alert destination types.
const (
	AlertWebhook = "webhook"
	AlertSlack   = "slack"
	AlertDiscord = "discord"
	AlertEmail   = "email"
)

// SMTP transport security.
const (
	SMTPNone     = "none"     // plain, port 25
	SMTPStartTLS = "starttls" // upgrade on port 587
	SMTPTLS      = "tls"      // implicit TLS, port 465
)

var (
	alertTypes    = map[string]bool{AlertWebhook: true, AlertSlack: true, AlertDiscord: true, AlertEmail: true}
	smtpSecurity  = map[string]bool{SMTPNone: true, SMTPStartTLS: true, SMTPTLS: true}
	webhookTypes  = map[string]bool{AlertWebhook: true, AlertSlack: true, AlertDiscord: true}
	alertTypeName = map[string]string{
		AlertWebhook: "Webhook", AlertSlack: "Slack", AlertDiscord: "Discord", AlertEmail: "Email",
	}
)

// A Destination is one place session alerts are delivered.
type Destination struct {
	ID      int64
	Name    string
	Type    string
	Enabled bool
	URL     string // webhook / slack / discord

	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTo       string
	SMTPSecurity string

	// Events is a comma-separated list of event kinds this destination wants.
	// Empty means every kind — the common case.
	Events string
}

// AlertEventKind is one selectable event a destination can filter on, with the
// label shown in the UI.
type AlertEventKind struct {
	Kind, Label string
}

// AlertEventKinds are the kinds a destination may subscribe to.
func AlertEventKinds() []AlertEventKind {
	return []AlertEventKind{
		{EventSessionDown, "Session down"},
		{EventSessionUp, "Session recovery"},
		{EventFlap, "Flap"},
		{EventLimitHit, "Import limit"},
		{EventBirdUnreach, "BIRD unreachable"},
		{EventConfigApply, "Config applied"},
		{EventConfigRevert, "Config reverted"},
	}
}

// Wants reports whether this destination should receive an event of this kind.
func (d Destination) Wants(kind string) bool {
	if strings.TrimSpace(d.Events) == "" {
		return true
	}
	for _, k := range strings.Split(d.Events, ",") {
		if strings.TrimSpace(k) == kind {
			return true
		}
	}
	return false
}

// HasEvent reports whether a kind is in this destination's filter, for
// pre-checking the boxes on the edit form.
func (d Destination) HasEvent(kind string) bool {
	for _, k := range strings.Split(d.Events, ",") {
		if strings.TrimSpace(k) == kind {
			return true
		}
	}
	return false
}

func (d Destination) IsWebhookKind() bool { return webhookTypes[d.Type] }
func (d Destination) IsEmail() bool       { return d.Type == AlertEmail }
func (d Destination) TypeName() string    { return alertTypeName[d.Type] }

// Target is a short human summary of where this destination sends, for lists.
func (d Destination) Target() string {
	if d.IsEmail() {
		return d.SMTPTo
	}
	return d.URL
}

func (d *Destination) Validate() map[string]string {
	errs := map[string]string{}

	d.Name = strings.TrimSpace(d.Name)
	switch {
	case d.Name == "":
		errs["name"] = "Give this destination a name."
	case len(d.Name) > 64 || strings.ContainsAny(d.Name, "\n\r"):
		errs["name"] = "Keep the name short and on one line."
	}
	if !alertTypes[d.Type] {
		errs["type"] = "Choose webhook, Slack, Discord or email."
		return errs
	}

	if d.IsWebhookKind() {
		d.zeroSMTP()
		d.URL = strings.TrimSpace(d.URL)
		if u, err := url.Parse(d.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs["url"] = "Enter the https webhook URL from " + d.TypeName() + "."
		}
		return errs
	}

	// email
	d.URL = ""
	if d.SMTPHost = strings.TrimSpace(d.SMTPHost); d.SMTPHost == "" {
		errs["smtpHost"] = "Enter the SMTP server host."
	}
	if d.SMTPPort < 1 || d.SMTPPort > 65535 {
		errs["smtpPort"] = "Enter a port between 1 and 65535 (587 for STARTTLS, 465 for TLS, 25 for none)."
	}
	if !smtpSecurity[d.SMTPSecurity] {
		errs["smtpSecurity"] = "Choose none, STARTTLS or TLS."
	}
	if !looksLikeEmail(strings.TrimSpace(d.SMTPFrom)) {
		errs["smtpFrom"] = "Enter the address alerts are sent from."
	} else {
		d.SMTPFrom = strings.TrimSpace(d.SMTPFrom)
	}
	tos := SplitAddresses(d.SMTPTo)
	if len(tos) == 0 {
		errs["smtpTo"] = "Enter at least one recipient address."
	} else {
		for _, a := range tos {
			if !looksLikeEmail(a) {
				errs["smtpTo"] = fmt.Sprintf("%q is not a valid address.", a)
			}
		}
		d.SMTPTo = strings.Join(tos, ", ")
	}
	return errs
}

func (d *Destination) zeroSMTP() {
	d.SMTPHost, d.SMTPUsername, d.SMTPPassword, d.SMTPFrom, d.SMTPTo = "", "", "", "", ""
	d.SMTPPort, d.SMTPSecurity = 587, SMTPStartTLS
}

// SplitAddresses splits a comma/space/newline-separated recipient list.
func SplitAddresses(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	var out []string
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// looksLikeEmail is a deliberately loose check: the SMTP server is the real
// validator, and birdy should not reject an address its mailserver would accept.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.ContainsAny(s, " \n\r") {
		return false
	}
	// A bare "user@host" is fine; a host with a dot or a literal IP is typical.
	host := s[at+1:]
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return true
	}
	return strings.Contains(host, ".")
}

const destCols = `id, name, type, enabled, url, smtp_host, smtp_port, smtp_username,
	smtp_password, smtp_from, smtp_to, smtp_security, events`

func scanDestination(sc scanner) (Destination, error) {
	var d Destination
	err := sc.Scan(&d.ID, &d.Name, &d.Type, &d.Enabled, &d.URL, &d.SMTPHost, &d.SMTPPort,
		&d.SMTPUsername, &d.SMTPPassword, &d.SMTPFrom, &d.SMTPTo, &d.SMTPSecurity, &d.Events)
	return d, err
}

func (s *Store) ListAlertDestinations() ([]Destination, error) {
	rows, err := s.db.Query(`SELECT ` + destCols + ` FROM alert_destinations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list alert destinations: %w", err)
	}
	defer rows.Close()
	var out []Destination
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EnabledAlertDestinations is what the notifier delivers to.
func (s *Store) EnabledAlertDestinations() ([]Destination, error) {
	all, err := s.ListAlertDestinations()
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, d := range all {
		if d.Enabled {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Store) GetAlertDestination(id int64) (Destination, error) {
	row := s.db.QueryRow(`SELECT `+destCols+` FROM alert_destinations WHERE id = ?`, id)
	d, err := scanDestination(row)
	if err == sql.ErrNoRows {
		return Destination{}, ErrNotFound
	}
	return d, err
}

func (s *Store) CreateAlertDestination(d Destination) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO alert_destinations (name, type, enabled, url, smtp_host, smtp_port,
			smtp_username, smtp_password, smtp_from, smtp_to, smtp_security, events, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Name, d.Type, d.Enabled, d.URL, d.SMTPHost, d.SMTPPort,
		d.SMTPUsername, d.SMTPPassword, d.SMTPFrom, d.SMTPTo, d.SMTPSecurity, d.Events, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create alert destination: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateAlertDestination(d Destination) error {
	res, err := s.db.Exec(`
		UPDATE alert_destinations SET name = ?, type = ?, enabled = ?, url = ?, smtp_host = ?,
			smtp_port = ?, smtp_username = ?, smtp_password = ?, smtp_from = ?, smtp_to = ?,
			smtp_security = ?, events = ?, updated_at = ?
		WHERE id = ?`,
		d.Name, d.Type, d.Enabled, d.URL, d.SMTPHost, d.SMTPPort, d.SMTPUsername, d.SMTPPassword,
		d.SMTPFrom, d.SMTPTo, d.SMTPSecurity, d.Events, now(), d.ID)
	if err != nil {
		return fmt.Errorf("store: update alert destination: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeleteAlertDestination(id int64) error {
	res, err := s.db.Exec(`DELETE FROM alert_destinations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete alert destination: %w", err)
	}
	return affectedOne(res)
}
