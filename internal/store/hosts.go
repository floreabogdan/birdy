package store

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParseAllowedHosts reads the allowed-host-names textarea — one name per line
// or comma-separated, blank lines and # comments ignored — into lower-case names
// without a trailing dot, the form a Host header is compared in. Returns the
// names and a list of human-readable errors.
//
// The list exists to stop DNS rebinding, where a hostile page points its own
// name at birdy. IP addresses and localhost cannot be rebound, so birdy accepts
// them without being told; this list is only for the names operators type.
func ParseAllowedHosts(text string) ([]string, []string) {
	var out, errs []string
	line := 0
	for raw := range strings.Lines(text) {
		line++
		s := strings.TrimSpace(raw)
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" {
			continue
		}
		for _, tok := range strings.Split(s, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			name, msg := parseHostName(tok)
			if msg != "" {
				errs = append(errs, fmt.Sprintf("Line %d: %s", line, msg))
				continue
			}
			out = append(out, name)
		}
	}
	return out, errs
}

func parseHostName(tok string) (string, string) {
	name := strings.TrimSuffix(strings.ToLower(tok), ".")
	if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return "", fmt.Sprintf("%q is an IP address. IP addresses are always accepted; list only names.", tok)
	}
	switch {
	case strings.ContainsAny(name, ":/[]"):
		return "", fmt.Sprintf("%q: enter just the host name, without a scheme, port or path (e.g. router1.example.net).", tok)
	case strings.Contains(name, "*"):
		return "", fmt.Sprintf("%q: wildcards are not supported; list each name.", tok)
	case !validHostName(name):
		return "", fmt.Sprintf("%q is not a host name.", tok)
	}
	return name, ""
}

// validHostName accepts DNS labels of letters, digits, hyphens and — because
// internal zones and browsers both tolerate them — underscores. An IDN must be
// entered in its xn-- form, which is what a browser puts in the Host header.
func validHostName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// SaveAllowedHosts updates only the allowed host names, so the settings forms
// cannot clobber each other's fields.
func (s *Store) SaveAllowedHosts(text string) error {
	res, err := s.db.Exec(`UPDATE settings SET allowed_hosts = ?, updated_at = ? WHERE id = 1`, text, now())
	if err != nil {
		return fmt.Errorf("store: save allowed hosts: %w", err)
	}
	return affectedOne(res)
}
