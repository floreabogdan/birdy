package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/federation"
	"github.com/floreabogdan/birdy/internal/store"
)

const instanceCookieName = "birdy_instance"

// maxConcurrentInstancePolls caps how many remote instances we contact at once
// so a large fleet cannot spawn an unbounded burst of goroutines and sockets.
const maxConcurrentInstancePolls = 8

type instanceView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"baseURL,omitempty"`
	GroupName string `json:"group,omitempty"`
	Tags      string `json:"tags,omitempty"`
	Status    string `json:"status,omitempty"`
	// No omitempty: a genuine 0 ms reading (a loopback target) must serialize,
	// or the client's `latencyMS >= 0` check drops the latency for a healthy
	// instance. Unchecked instances carry -1 and are correctly skipped.
	LatencyMS     int    `json:"latencyMS"`
	LastCheckAt   string `json:"lastCheckAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

type instancesPageView struct {
	Active    string
	ReadOnly  bool
	LocalName string
	Items     []instanceView
	Healthy   int
	Degraded  int
	Offline   int
	Msg       string
	Err       string
}

type instanceDetailPageView struct {
	Active   string
	ReadOnly bool
	Instance store.Instance
	Health   string
	View     DashboardView
	Err      string
}

func selectedInstanceID(r *http.Request) int64 {
	cookie, err := r.Cookie(instanceCookieName)
	if err != nil {
		return 0
	}
	id, err := strconv.ParseInt(cookie.Value, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func setSelectedInstance(w http.ResponseWriter, id int64, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: instanceCookieName, Value: strconv.FormatInt(id, 10), Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (s *Server) listInstanceViews() ([]instanceView, error) {
	instances, err := s.store.ListInstances()
	if err != nil {
		return nil, err
	}
	out := make([]instanceView, 0, len(instances))
	for _, i := range instances {
		out = append(out, instanceView{ID: i.ID, Name: i.Name, BaseURL: i.BaseURL, GroupName: i.GroupName, Tags: i.Tags, LatencyMS: i.LastLatencyMS, LastCheckAt: i.LastCheckAt, LastSuccessAt: i.LastSuccessAt, LastError: i.LastError, Status: healthStatus(i)})
	}
	return out, nil
}

func healthStatus(i store.Instance) string {
	if i.LastCheckAt == "" {
		return "unknown"
	}
	if i.LastError != "" {
		return "offline"
	}
	if i.LastLatencyMS >= 500 {
		return "degraded"
	}
	return "healthy"
}

func (s *Server) refreshInstanceHealth(ctx context.Context) {
	// Serialize refreshes: the background loop and an operator-triggered refresh
	// must not overlap, or each independently reads the pre-transition state and
	// both emit a duplicate up/down alert for one change.
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	instances, err := s.store.ListInstances()
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentInstancePolls)
	for _, instance := range instances {
		instance := instance
		sem <- struct{}{} // acquire before spawning so goroutine count is bounded too
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			latency, checkErr := (federation.Client{BaseURL: instance.BaseURL, Token: instance.Token}).Check(checkCtx)
			if checkErr != nil && ctx.Err() != nil {
				// The whole batch was cancelled (shutdown, or the handler's own
				// timeout) — not evidence the instance is down. Don't persist a
				// false outage or fire a spurious offline alert.
				return
			}
			checked := time.Now().UTC().Format(time.RFC3339Nano)
			success := instance.LastSuccessAt
			latencyMS := int(latency / time.Millisecond)
			lastError := ""
			if checkErr != nil {
				lastError = checkErr.Error()
			} else {
				success = checked
			}
			oldStatus := healthStatus(instance)
			if err := s.store.UpdateInstanceHealth(instance.ID, checked, success, latencyMS, lastError); err != nil {
				s.log.Warn("could not persist instance health", "instance", instance.Name, "error", err)
			}
			newStatus := "offline"
			if checkErr == nil {
				newStatus = healthStatus(store.Instance{LastCheckAt: checked, LastLatencyMS: latencyMS})
			}
			if oldStatus != "unknown" && oldStatus != newStatus {
				if newStatus == "offline" {
					s.emitEvent(store.EventInstanceDown, instance.Name, "Remote Birdy instance "+instance.Name+" is unreachable: "+lastError)
				} else if oldStatus == "offline" {
					s.emitEvent(store.EventInstanceUp, instance.Name, "Remote Birdy instance "+instance.Name+" is reachable again")
				}
			}
		}()
	}
	wg.Wait()
}

func (s *Server) handleInstancesPage(w http.ResponseWriter, r *http.Request) {
	items, err := s.listInstanceViews()
	if err != nil {
		s.serverError(w, "list instances", err)
		return
	}
	v := instancesPageView{Active: "instances", ReadOnly: s.readOnly, LocalName: s.localInstanceName(), Items: items, Msg: r.URL.Query().Get("flash"), Err: r.URL.Query().Get("err")}
	for _, item := range items {
		switch item.Status {
		case "healthy":
			v.Healthy++
		case "degraded":
			v.Degraded++
		case "offline":
			v.Offline++
		}
	}
	render(w, s.log, "instances.html", v)
}

func (s *Server) handleInstanceDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	instance, ok, err := s.store.GetInstance(id)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	view := instanceDetailPageView{Active: "instances", ReadOnly: s.readOnly, Instance: instance, Health: healthStatus(instance)}
	clone := r.Clone(r.Context())
	clone.AddCookie(&http.Cookie{Name: instanceCookieName, Value: strconv.FormatInt(id, 10)})
	view.View = s.selectedDashboardView(clone)
	view.Err = view.View.PollErr
	render(w, s.log, "instance_detail.html", view)
}

func (s *Server) localInstanceName() string {
	if settings, ok, err := s.store.GetSettings(); err == nil && ok && strings.TrimSpace(settings.RouterLabel) != "" {
		return settings.RouterLabel
	}
	return "This Birdy"
}

func validateInstanceURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("use an http(s) URL without credentials, query parameters, or a fragment")
	}
	// Reject targets that could only be an SSRF pivot, not a real remote Birdy:
	// loopback, the link-local metadata range (169.254/16, fe80::), the
	// unspecified address, and multicast. Private/ULA ranges are allowed on
	// purpose — routers commonly observe each other over management networks.
	if addr, perr := netip.ParseAddr(u.Hostname()); perr == nil {
		if blockedInstanceAddr(addr) {
			return "", fmt.Errorf("that address cannot be a remote Birdy target")
		}
	} else {
		// A DNS name could resolve into a blocked range (the classic
		// metadata.internal SSRF the literal check above cannot see). Resolve and
		// apply the same block to every address it maps to. A lookup failure is
		// left to the connection attempt rather than blocking a temporarily
		// unresolvable target from being added; a rebind between this check and a
		// later fetch is a residual risk under the operator-controlled model.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if addrs, lerr := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname()); lerr == nil {
			for _, addr := range addrs {
				if blockedInstanceAddr(addr) {
					return "", fmt.Errorf("that host resolves to an address that cannot be a remote Birdy target")
				}
			}
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// blockedInstanceAddr reports whether an address can only be an SSRF pivot
// rather than a reachable remote Birdy: loopback, link-local (including the
// cloud metadata range), unspecified, or multicast.
func blockedInstanceAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast()
}

func normalizeInstanceTags(raw string) (string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		tag := strings.ToLower(strings.TrimSpace(part))
		if tag == "" || seen[tag] {
			continue
		}
		if len(tag) > 32 || strings.ContainsAny(tag, "\r\n\t") {
			return "", fmt.Errorf("tags must be short, comma-separated labels")
		}
		seen[tag] = true
		out = append(out, tag)
	}
	joined := strings.Join(out, ", ")
	if len(joined) > 256 {
		return "", fmt.Errorf("tags are limited to 256 characters")
	}
	return joined, nil
}

func (s *Server) handleInstanceAdd(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name, token := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("token"))
	groupName := strings.TrimSpace(r.FormValue("group"))
	tags, tagErr := normalizeInstanceTags(r.FormValue("tags"))
	baseURL, urlErr := validateInstanceURL(r.FormValue("baseURL"))
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n") {
		http.Redirect(w, r, "/instances?err="+flash("Enter a unique name of 64 characters or fewer"), http.StatusSeeOther)
		return
	}
	if urlErr != nil {
		http.Redirect(w, r, "/instances?err="+flash(urlErr.Error()), http.StatusSeeOther)
		return
	}
	if len(token) < 32 || len(token) > 4096 {
		http.Redirect(w, r, "/instances?err="+flash("The read-only API token looks invalid"), http.StatusSeeOther)
		return
	}
	if tagErr != nil || len(groupName) > 64 || strings.ContainsAny(groupName, "\r\n") {
		http.Redirect(w, r, "/instances?err="+flash("Group and tags are limited to short single-line values"), http.StatusSeeOther)
		return
	}
	if _, err := s.store.CreateInstanceWithMetadata(name, baseURL, token, groupName, tags); err != nil {
		if isUniqueViolation(err) {
			http.Redirect(w, r, "/instances?err="+flash("That instance name is already in use"), http.StatusSeeOther)
			return
		}
		s.serverError(w, "create instance", err)
		return
	}
	s.audit(r, "Added remote Birdy instance "+name)
	http.Redirect(w, r, "/instances?flash="+flash("Instance added"), http.StatusSeeOther)
}

func (s *Server) handleInstanceMetadata(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, ok, err := s.store.GetInstance(id); err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	groupName := strings.TrimSpace(r.FormValue("group"))
	tags, tagErr := normalizeInstanceTags(r.FormValue("tags"))
	if tagErr != nil || len(groupName) > 64 || strings.ContainsAny(groupName, "\r\n") {
		http.Redirect(w, r, "/instances?err="+flash("Group and tags are limited to short single-line values"), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateInstanceMetadata(id, groupName, tags); err != nil {
		s.serverError(w, "update instance metadata", err)
		return
	}
	s.audit(r, "Updated metadata for remote Birdy instance")
	http.Redirect(w, r, "/instances?flash="+flash("Instance metadata saved"), http.StatusSeeOther)
}

func (s *Server) handleInstanceDelete(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	instance, ok, err := s.store.GetInstance(id)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteInstance(id); err != nil {
		s.serverError(w, "delete instance", err)
		return
	}
	if selectedInstanceID(r) == id {
		setSelectedInstance(w, 0, s.cookieSecure(r))
	}
	s.audit(r, "Removed remote Birdy instance "+instance.Name)
	http.Redirect(w, r, "/instances?flash="+flash("Instance removed"), http.StatusSeeOther)
}

func (s *Server) handleInstanceRename(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	instance, ok, err := s.store.GetInstance(id)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n") {
		http.Redirect(w, r, "/instances?err="+flash("Use a friendly name of 1 to 64 characters"), http.StatusSeeOther)
		return
	}
	if err := s.store.RenameInstance(id, name); err != nil {
		if isUniqueViolation(err) {
			http.Redirect(w, r, "/instances?err="+flash("That instance name is already in use"), http.StatusSeeOther)
			return
		}
		s.serverError(w, "rename instance", err)
		return
	}
	s.audit(r, "Renamed remote Birdy instance "+instance.Name+" to "+name)
	http.Redirect(w, r, "/instances?flash="+flash("Instance renamed"), http.StatusSeeOther)
}

func (s *Server) handleLocalInstanceRename(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n") {
		http.Redirect(w, r, "/instances?err="+flash("Use a friendly name of 1 to 64 characters"), http.StatusSeeOther)
		return
	}
	settings, ok, err := s.store.GetSettings()
	if err != nil || !ok {
		s.serverError(w, "get settings", err)
		return
	}
	old := settings.RouterLabel
	settings.RouterLabel = name
	if err := s.store.SaveSettings(settings); err != nil {
		s.serverError(w, "rename local instance", err)
		return
	}
	s.audit(r, "Renamed local Birdy instance "+old+" to "+name)
	http.Redirect(w, r, "/instances?flash="+flash("Local instance renamed"), http.StatusSeeOther)
}

func (s *Server) handleInstanceSelect(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id < 0 {
		http.Redirect(w, r, "/?err="+flash("Unknown Birdy instance"), http.StatusSeeOther)
		return
	}
	if id > 0 {
		if _, ok, err := s.store.GetInstance(id); err != nil || !ok {
			http.Redirect(w, r, "/?err="+flash("Unknown Birdy instance"), http.StatusSeeOther)
			return
		}
	}
	setSelectedInstance(w, id, s.cookieSecure(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) apiInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	items, err := s.listInstanceViews()
	if err != nil {
		s.serverError(w, "list instances", err)
		return
	}
	writeJSON(w, struct {
		Selected int64          `json:"selected"`
		Local    instanceView   `json:"local"`
		Remote   []instanceView `json:"remote"`
	}{Selected: selectedInstanceID(r), Local: instanceView{ID: 0, Name: s.localInstanceName()}, Remote: items})
}

func (s *Server) apiInstancesRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	s.refreshInstanceHealth(ctx)
	w.Header().Set("Cache-Control", "no-store")
	items, err := s.listInstanceViews()
	if err != nil {
		s.serverError(w, "list refreshed instances", err)
		return
	}
	writeJSON(w, items)
}

func (s *Server) apiInstanceTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid form")
		return
	}
	baseURL, err := validateInstanceURL(r.FormValue("baseURL"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if len(token) < 32 || len(token) > 4096 {
		writeJSONError(w, http.StatusBadRequest, "the read-only API token looks invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	latency, checkErr := (federation.Client{BaseURL: baseURL, Token: token}).Check(ctx)
	response := map[string]any{"ok": checkErr == nil, "latencyMS": int(latency / time.Millisecond)}
	if checkErr != nil {
		response["error"] = checkErr.Error()
	}
	if checkErr != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, response)
		return
	}
	writeJSON(w, response)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": message})
}

type instanceActivityItem struct {
	Instance string          `json:"instance"`
	Event    json.RawMessage `json:"event"`
	Ts       time.Time       `json:"-"`
}

func (s *Server) apiInstanceActivity(w http.ResponseWriter, r *http.Request) {
	const limit = 8
	localEvents, err := s.store.ListEvents(limit, 0)
	if err != nil {
		s.serverError(w, "list local activity", err)
		return
	}
	items := make([]instanceActivityItem, 0, limit)
	localName := s.localInstanceName()
	for _, event := range localEvents {
		encoded, _ := json.Marshal(event)
		items = append(items, instanceActivityItem{Instance: localName, Event: encoded, Ts: event.Ts})
	}
	instances, err := s.store.ListInstances()
	if err != nil {
		s.serverError(w, "list remote activity targets", err)
		return
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentInstancePolls)
	for _, instance := range instances {
		instance := instance
		sem <- struct{}{} // acquire before spawning so goroutine count is bounded too
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			events, fetchErr := (federation.Client{BaseURL: instance.BaseURL, Token: instance.Token}).FetchEvents(ctx, limit)
			if fetchErr != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, event := range events {
				encoded, _ := json.Marshal(event)
				items = append(items, instanceActivityItem{Instance: instance.Name, Event: encoded, Ts: event.Ts})
			}
		}()
	}
	wg.Wait()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Ts.After(items[j].Ts) })
	if len(items) > 24 {
		items = items[:24]
	}
	writeJSON(w, items)
}

func (s *Server) handleSettingsInstanceToken(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	token, err := newSessionToken()
	if err != nil {
		s.serverError(w, "generate instance API token", err)
		return
	}
	expiresAt := ""
	switch r.FormValue("expiry") {
	case "30":
		expiresAt = time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "90":
		expiresAt = time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "365":
		expiresAt = time.Now().UTC().Add(365 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "never", "":
	default:
		http.Error(w, "invalid token expiry", http.StatusBadRequest)
		return
	}
	if _, err := s.store.CreateInstanceToken("Dashboard observer", store.HashInstanceAPIToken(token), "dashboard timeline", expiresAt); err != nil {
		s.serverError(w, "save instance API token", err)
		return
	}
	s.audit(r, "Generated a new read-only instance API token")
	s.renderSettings(w, SettingsView{Active: "settings", Tab: "general", APIToken: token, Msg: "Copy this token now. It will not be shown again."})
}

func (s *Server) handleSettingsInstanceTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	if err := s.store.RevokeInstanceAPIToken(); err != nil {
		s.serverError(w, "revoke instance API token", err)
		return
	}
	if err := s.store.RevokeAllInstanceTokens(); err != nil {
		s.serverError(w, "revoke instance API tokens", err)
		return
	}
	s.audit(r, "Revoked the read-only instance API token")
	http.Redirect(w, r, "/settings?tab=general&flash="+flash("Remote dashboard token revoked"), http.StatusSeeOther)
}

func (s *Server) handleSettingsInstanceTokenRevokeOne(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if err := s.store.RevokeInstanceToken(id); err != nil {
		s.serverError(w, "revoke instance token", err)
		return
	}
	s.audit(r, "Revoked a read-only instance API token")
	http.Redirect(w, r, "/settings?tab=general&flash="+flash("Remote dashboard token revoked"), http.StatusSeeOther)
}
