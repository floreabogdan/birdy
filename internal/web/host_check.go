package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

// hostAllowed is birdy's answer to DNS rebinding. A hostile page re-points its
// own name at birdy's address, and the operator's browser — on an allow-listed
// IP, or with the SSH tunnel to 127.0.0.1 open — then sends it requests that
// come from a trusted address and whose Origin matches their Host, because both
// are the attacker's name. The IP allow-list and the same-origin check pass; the
// Host is the one thing the attacker cannot make birdy's own.
//
// So a browser is answered only when it addresses birdy by an IP literal (which
// cannot be rebound: the browser connects to exactly that address), as
// localhost, or by a name the operator listed under Settings → Access control.
// An empty Host is accepted: only a non-browser HTTP/1.0 client sends none.
func (s *Server) hostAllowed(r *http.Request) bool {
	if r.Host == "" || hostCheckExempt(r) {
		return true
	}
	name := requestHostname(r.Host)
	if name == "localhost" {
		return true
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	s.accessMu.RLock()
	defer s.accessMu.RUnlock()
	return s.allowedHosts[name]
}

// hostCheckExempt lets machine clients through by any name, where rebinding
// cannot reach them:
//
//   - The dashboard and timeline API with a bearer token, which is how other
//     birdy instances read this one. A rebound page does not have the token, and
//     requireDashboardAuth answers a wrong one with 401 — it never falls back to
//     the session cookie when a Bearer header is present.
//   - /metrics and /healthz, unless the request comes from a browser. Prometheus
//     and health checkers commonly use the router's name and send no Fetch
//     Metadata; current browsers (Chrome, Firefox, Safari 16.4+) send Sec-Fetch-*
//     on every request, and a page's script cannot remove it. So existing scrape
//     configs keep working while a rebound page cannot read the session
//     inventory /metrics holds — except from an older browser, as before.
func hostCheckExempt(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/dashboard", "/api/events":
		_, ok := bearerToken(r)
		return ok
	case "/metrics", "/healthz":
		return r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Sec-Fetch-Mode") == ""
	}
	return false
}

// requestHostname reduces a Host header to the form names are compared in:
// lower case, no port, no IPv6 brackets, no trailing dot.
func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// refuseHost explains the refusal in plain text: the operator who meets it has
// usually just upgraded and browsed to birdy by name as they always have, and
// needs to know how to get in and where to add that name.
func (s *Server) refuseHost(w http.ResponseWriter, r *http.Request) {
	name := requestHostname(r.Host)
	if len(name) > 253 {
		name = name[:253]
	}
	scheme, byIP, port := "http", "", "8080"
	if r.TLS != nil {
		scheme = "https"
	}
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, p, err := net.SplitHostPort(local.String()); err == nil {
			byIP, port = fmt.Sprintf(" (%s://%s/)", scheme, local.String()), p
		}
	}
	msg := fmt.Sprintf(`birdy: refusing a request addressed to %q.

birdy answers a browser only when it is reached by IP address, as localhost,
or by a host name listed under Settings → Access control → Allowed host names.
Any other name could be a hostile web page pointing its own name at birdy
(DNS rebinding), so it is refused.

To get in, open birdy by its IP address%s, or through an SSH tunnel:
  ssh -L 8080:127.0.0.1:%s <this router>   then open http://127.0.0.1:8080/
Then add %q under Settings → Access control → Allowed host names.
`, name, byIP, port, name)
	http.Error(w, msg, http.StatusForbidden)
}

// handleSettingsHosts saves the allowed host names. Like the IP allow-list it
// is a birdy setting, not a BIRD write, so read-only mode allows it.
func (s *Server) handleSettingsHosts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	text := strings.ReplaceAll(r.FormValue("allowedHosts"), "\r\n", "\n")
	if _, errs := store.ParseAllowedHosts(text); len(errs) > 0 {
		s.renderSettings(w, SettingsView{
			Active: "settings", Tab: "access", ReadOnly: s.readOnly,
			AllowedHosts: text, HostsErr: strings.Join(errs, "\n"),
			ConnectingIP: clientAddr(r).String(),
		})
		return
	}
	if err := s.store.SaveAllowedHosts(text); err != nil {
		s.serverError(w, "save allowed hosts", err)
		return
	}
	s.reloadAccess()
	s.audit(r, "updated the allowed host names")
	// No warning for removing the name in use: the redirect below is then
	// refused with the plain-text explanation, and a flash cookie set for that
	// name would never be shown at the IP address the operator moves to.
	s.settingsRedirect(w, r, "access", "Allowed host names saved.")
}
