package web

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type SettingsView struct {
	Active         string
	Tab            string
	ReadOnly       bool
	Settings       store.Settings
	SocketPath     string
	ListenAddr     string
	LatestSnapshot string
	Msg            string
	Err            string
	IdentityErrs   map[string]string

	// Destinations backs the Alerts tab — the notification channels that used to
	// live on their own page.
	Destinations []store.Destination

	// The bogon lists live here rather than in the Library: generated filters
	// name them directly, so they are router settings, not composable objects.
	BogonsV4  string
	BogonsV6  string
	BogonASNs string
	BogonErrs map[string]string

	// RawConfig is the escape hatch, echoed back on a failed save so the
	// operator does not lose what they typed.
	RawConfig      string
	RawErr         string
	RawOutput      string // what bird -p said, when it rejected the config
	APIToken       string // newly generated remote-read token, shown once only
	InstanceTokens []store.InstanceToken

	// AccessWhitelist is the IP allow-list; ConnectingIP is the operator's own
	// address, shown so they can add it before restricting and not lock out.
	AccessWhitelist string
	AccessErr       string
	ConnectingIP    string
	// WideOpen: bound beyond loopback with an allow-all list. Flagged here, on the
	// page that fixes it, rather than on the dashboard — a warning you cannot act
	// on from where it appears is just noise you learn to skip.
	WideOpen bool
	TLS      bool
}

// settingsTabs are the tab keys in display order; the first is the default.
var settingsTabs = []string{"general", "theme", "bogons", "access", "alerts", "advanced"}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, SettingsView{
		Active: "settings", ReadOnly: s.readOnly,
		Tab: tabParam(r, settingsTabs...),
		Msg: r.URL.Query().Get("flash"), Err: r.URL.Query().Get("err"),
		ConnectingIP: clientAddr(r).String(),
	})
}

// settingsRedirect returns to a specific Settings tab with a flash message, so a
// save keeps the operator on the panel they were editing.
func settingsRedirect(w http.ResponseWriter, r *http.Request, tab, msg string) {
	http.Redirect(w, r, "/settings?tab="+tab+"&flash="+flash(msg), http.StatusSeeOther)
}

func (s *Server) renderSettings(w http.ResponseWriter, v SettingsView) {
	if v.Tab == "" {
		v.Tab = settingsTabs[0]
	}
	if dests, err := s.store.ListAlertDestinations(); err == nil {
		v.Destinations = dests
	}
	if tokens, err := s.store.ListInstanceTokens(); err == nil {
		v.InstanceTokens = tokens
	}
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		// A rejected form keeps what the user typed; otherwise show what's stored.
		if len(v.IdentityErrs) == 0 {
			v.Settings = settings
		} else {
			stored := settings
			stored.RouterLabel = v.Settings.RouterLabel
			stored.RouterID = v.Settings.RouterID
			stored.LocalASN = v.Settings.LocalASN
			stored.RRClusterID = v.Settings.RRClusterID
			stored.KernelPrefSrcV4 = v.Settings.KernelPrefSrcV4
			stored.KernelPrefSrcV6 = v.Settings.KernelPrefSrcV6
			stored.KernelExportBGPV4 = v.Settings.KernelExportBGPV4
			stored.KernelExportBGPV6 = v.Settings.KernelExportBGPV6
			v.Settings = stored
		}
	}
	v.WideOpen = s.WideOpen()
	v.TLS = s.tls
	if s.snap != nil {
		if p, ok := s.snap.LatestSnapshot(); ok {
			v.LatestSnapshot = p
		}
	}
	if v.RawErr == "" {
		if settings, ok, err := s.store.GetSettings(); err == nil && ok {
			v.RawConfig = settings.RawConfig
		}
	}
	if v.AccessErr == "" {
		if settings, ok, err := s.store.GetSettings(); err == nil && ok {
			v.AccessWhitelist = settings.AccessWhitelist
		}
	}
	// A rejected bogon edit keeps the text the user typed; otherwise load stored.
	if len(v.BogonErrs) == 0 {
		if ps, err := s.store.GetBogonSet(store.FamilyV4); err == nil {
			v.BogonsV4 = formatEntries(ps.Entries)
		}
		if ps, err := s.store.GetBogonSet(store.FamilyV6); err == nil {
			v.BogonsV6 = formatEntries(ps.Entries)
		}
		if list, err := s.store.ListBogonASNs(); err == nil {
			v.BogonASNs = store.FormatBogonASNs(list)
		}
	}
	render(w, s.log, "settings.html", v)
}

func formatEntries(entries []store.PrefixEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Pattern())
		b.WriteString("\n")
	}
	return b.String()
}

// handleSettingsBogons rewrites both bogon prefix sets and the bogon ASN list
// in one go. Nothing is saved unless all three parse: a half-applied bogon list
// is worse than none, because generated filters name these sets directly.
func (s *Server) handleSettingsBogons(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	v4Text, v6Text, asnText := r.FormValue("bogonsV4"), r.FormValue("bogonsV6"), r.FormValue("bogonAsns")
	if r.FormValue("restore") == "defaults" {
		v4Text, v6Text = defaultBogonText(store.FamilyV4), defaultBogonText(store.FamilyV6)
		asnText = store.FormatBogonASNs(store.DefaultBogonASNs())
	}

	errs := map[string]string{}

	v4, err := s.store.GetBogonSet(store.FamilyV4)
	if err != nil {
		s.serverError(w, "get bogon set", err)
		return
	}
	v6, err := s.store.GetBogonSet(store.FamilyV6)
	if err != nil {
		s.serverError(w, "get bogon set", err)
		return
	}
	v4.Entries, v6.Entries = parseEntries(v4Text), parseEntries(v6Text)
	for field, ps := range map[string]*store.PrefixSet{"bogonsV4": &v4, "bogonsV6": &v6} {
		for k, msg := range ps.Validate() {
			// Name/family/originate cannot change here, so only entry errors matter.
			if strings.HasPrefix(k, "entry.") || k == "entries" {
				if errs[field] == "" {
					errs[field] = msg
				} else {
					errs[field] += "\n" + msg
				}
			}
		}
	}

	asns, asnErrs := store.ParseBogonASNs(asnText)
	for _, msg := range asnErrs {
		if errs["bogonAsns"] == "" {
			errs["bogonAsns"] = msg
		} else {
			errs["bogonAsns"] += "\n" + msg
		}
	}

	if len(errs) > 0 {
		v := SettingsView{Active: "settings", Tab: "bogons", ReadOnly: s.readOnly, BogonErrs: errs,
			BogonsV4: v4Text, BogonsV6: v6Text, BogonASNs: asnText}
		s.renderSettings(w, v)
		return
	}

	if err := s.store.UpdatePrefixSet(v4); err != nil {
		s.serverError(w, "save bogons v4", err)
		return
	}
	if err := s.store.UpdatePrefixSet(v6); err != nil {
		s.serverError(w, "save bogons v6", err)
		return
	}
	if err := s.store.ReplaceBogonASNs(asns); err != nil {
		s.serverError(w, "save bogon ASNs", err)
		return
	}
	settingsRedirect(w, r, "bogons", "Bogon lists saved")
}

// parseEntries reads the prefix textarea, ignoring blanks and # comments.
func parseEntries(text string) []store.PrefixEntry {
	var out []store.PrefixEntry
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, modifier := splitPattern(line)
		out = append(out, store.PrefixEntry{Prefix: prefix, Modifier: modifier})
	}
	return out
}

func defaultBogonText(family string) string {
	var b strings.Builder
	for _, p := range store.DefaultBogonPrefixes(family) {
		b.WriteString(p)
		b.WriteString("+\n")
	}
	return b.String()
}

// handleSettingsIdentity saves the router ID and local ASN. Both are inputs to
// the config renderer, not readings taken from BIRD: the model is what the
// router should be, and bird.conf is derived from it.
func (s *Server) handleSettingsIdentity(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	settings, ok, err := s.store.GetSettings()
	if err != nil || !ok {
		s.serverError(w, "get settings", err)
		return
	}

	errs := map[string]string{}
	// The label only names the router to a human — in alerts and the config-backup
	// mail — so it is free text. It still has to survive a JSON payload and an
	// email header intact, which line breaks would not.
	label := strings.TrimSpace(r.FormValue("routerLabel"))
	switch {
	case len(label) > 64:
		errs["routerLabel"] = "Keep the label under 64 characters."
	case strings.ContainsAny(label, "\r\n"):
		errs["routerLabel"] = "Line breaks are not allowed."
	}
	routerID := strings.TrimSpace(r.FormValue("routerId"))
	if addr, err := netip.ParseAddr(routerID); err != nil || !addr.Is4() {
		errs["routerId"] = "A BGP router ID is a 32-bit value, written as an IPv4 address."
	}
	asn, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("localAsn")), 10, 64)
	if err != nil || asn < 1 || asn > 4294967295 {
		errs["localAsn"] = "Enter an AS number between 1 and 4294967295."
	}
	probe := store.Settings{
		RRClusterID:       r.FormValue("rrClusterId"),
		KernelPrefSrcV4:   r.FormValue("kernelPrefsrcV4"),
		KernelPrefSrcV6:   r.FormValue("kernelPrefsrcV6"),
		KernelExportBGPV4: r.FormValue("kernelExportBgpV4") == "on",
		KernelExportBGPV6: r.FormValue("kernelExportBgpV6") == "on",
	}
	if msg := probe.ValidateRRClusterID(); msg != "" {
		errs["rrClusterId"] = msg
	}
	if msg := probe.ValidateKernelPrefSrcV4(); msg != "" {
		errs["kernelPrefsrcV4"] = msg
	}
	if msg := probe.ValidateKernelPrefSrcV6(); msg != "" {
		errs["kernelPrefsrcV6"] = msg
	}

	if len(errs) > 0 {
		v := SettingsView{Active: "settings", Tab: "general", ReadOnly: s.readOnly, IdentityErrs: errs}
		v.Settings.RouterLabel = label
		v.Settings.RouterID = routerID
		v.Settings.LocalASN = sql.NullInt64{Int64: asn, Valid: errs["localAsn"] == ""}
		v.Settings.RRClusterID = probe.RRClusterID
		v.Settings.KernelPrefSrcV4 = probe.KernelPrefSrcV4
		v.Settings.KernelPrefSrcV6 = probe.KernelPrefSrcV6
		v.Settings.KernelExportBGPV4 = probe.KernelExportBGPV4
		v.Settings.KernelExportBGPV6 = probe.KernelExportBGPV6
		s.renderSettings(w, v)
		return
	}

	settings.RouterLabel = label
	settings.RouterID = routerID
	settings.LocalASN = sql.NullInt64{Int64: asn, Valid: true}
	settings.RRClusterID = probe.RRClusterID
	settings.KernelPrefSrcV4 = probe.KernelPrefSrcV4
	settings.KernelPrefSrcV6 = probe.KernelPrefSrcV6
	settings.KernelExportBGPV4 = probe.KernelExportBGPV4
	settings.KernelExportBGPV6 = probe.KernelExportBGPV6
	if err := s.store.SaveSettings(settings); err != nil {
		s.serverError(w, "save settings", err)
		return
	}
	settingsRedirect(w, r, "general", "Router identity saved")
}

// maxRawConfig bounds the escape hatch. It is a router config, not a document;
// anything approaching this is a sign the model should grow instead.
const maxRawConfig = 64 << 10

// handleSettingsRaw saves the raw config block, but only if BIRD can still parse
// the whole file with it in place. birdy understands nothing about what is in
// here, so `bird -p` is the only gate — and it has to run against the complete
// rendered config, not the fragment, because a stray brace only shows up in
// context.
func (s *Server) handleSettingsRaw(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	raw := strings.ReplaceAll(r.FormValue("rawConfig"), "\r\n", "\n")

	fail := func(msg, output string) {
		s.renderSettings(w, SettingsView{
			Active: "settings", Tab: "advanced", ReadOnly: s.readOnly,
			RawConfig: raw, RawErr: msg, RawOutput: output,
		})
	}
	if len(raw) > maxRawConfig {
		fail(fmt.Sprintf("Too long: %d bytes, limit is %d.", len(raw), maxRawConfig), "")
		return
	}
	if strings.ContainsRune(raw, 0) {
		fail("Contains a NUL byte.", "")
		return
	}

	// Masked: the password is a string literal either way, so a masked config
	// proves the syntax just as well without writing real secrets to a temp file.
	in, reason, err := s.renderInput(true)
	if err != nil {
		s.serverError(w, "build render input", err)
		return
	}
	in.RawConfig = raw

	// Without a router identity there is no config to check this against. Save
	// it anyway and say so, rather than blocking on an unrelated setting.
	unchecked := reason != ""
	if !unchecked {
		candidate, err := birdconf.Config(in)
		if err != nil {
			fail("The config cannot be rendered: "+err.Error(), "")
			return
		}
		// Skipped means no bird binary here — nothing was proven, so do not
		// pretend the block was rejected.
		if res := birdconf.Check(r.Context(), s.birdBinary, candidate); res.Skipped == "" && !res.OK {
			fail("BIRD rejected the config with this block in place. Nothing was saved.", res.Output)
			return
		}
	}

	if err := s.store.SaveRawConfig(raw); err != nil {
		s.serverError(w, "save raw config", err)
		return
	}
	msg := "Raw config saved"
	if unchecked {
		msg = "Raw config saved, but not checked: " + reason
	}
	settingsRedirect(w, r, "advanced", msg)
}

// handleSettingsAccess saves the access whitelist. It is a birdy setting, not a
// BIRD write, so it is allowed in read-only mode (that is when the exposure it
// closes matters most). The new list takes effect immediately, and if it would
// exclude the operator's own address they are warned — though loopback (an SSH
// tunnel) is never blocked, so they cannot truly lock themselves out.
func (s *Server) handleSettingsAccess(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	text := strings.ReplaceAll(r.FormValue("accessWhitelist"), "\r\n", "\n")

	prefixes, errs := store.ParseAccessWhitelist(text)
	if len(errs) > 0 {
		s.renderSettings(w, SettingsView{
			Active: "settings", Tab: "access", ReadOnly: s.readOnly,
			AccessWhitelist: text, AccessErr: strings.Join(errs, "\n"),
			ConnectingIP: clientAddr(r).String(),
		})
		return
	}
	if err := s.store.SaveAccessWhitelist(text); err != nil {
		s.serverError(w, "save access whitelist", err)
		return
	}
	s.reloadAccess() // take effect immediately

	msg := "Access whitelist saved."
	if ip := clientAddr(r); ip.IsValid() && !ip.IsLoopback() && !store.AccessAllowed(prefixes, ip) {
		msg = "Access whitelist saved — heads up: your current IP " + ip.String() +
			" is not in it. You can still reach birdy over an SSH tunnel (loopback is always allowed)."
	}
	s.audit(r, "updated the access whitelist")
	settingsRedirect(w, r, "access", msg)
}

// apiSnapshotDownload always produces a fresh, consistent snapshot on demand
// rather than serving a possibly-stale nightly one, and streams it straight
// back — nothing is left behind on disk beyond the usual retained set.
func (s *Server) apiSnapshotDownload(w http.ResponseWriter, r *http.Request) {
	if s.snap == nil {
		http.Error(w, "snapshots not configured", http.StatusServiceUnavailable)
		return
	}
	path, err := s.snap.CreateSnapshot(s.store)
	if err != nil {
		s.log.Error("snapshot creation failed", "error", err)
		http.Error(w, "failed to create snapshot", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.log.Error("snapshot open failed", "error", err)
		http.Error(w, "failed to open snapshot", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	_, _ = io.Copy(w, f)
}

func (s *Server) apiSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if !s.writeGuard(w) {
		return
	}
	if s.snap == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "snapshots not configured")
		return
	}
	const maxSnapshotUpload = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotUpload+(1<<20))
	if err := r.ParseMultipartForm(maxSnapshotUpload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	file, _, err := r.FormFile("db")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing file field \"db\"")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotUpload+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	if len(data) > maxSnapshotUpload {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "snapshot exceeds 64 MiB limit")
		return
	}
	if err := s.snap.StageRestore(data); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{
		"status":  "staged",
		"message": "Restore staged. Restart birdy (systemctl restart birdy) to apply it.",
	})
}
