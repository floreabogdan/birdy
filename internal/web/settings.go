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
	ReadOnly       bool
	Settings       store.Settings
	SocketPath     string
	ListenAddr     string
	LatestSnapshot string
	Msg            string
	Err            string
	IdentityErrs   map[string]string

	// The bogon lists live here rather than in the Library: generated filters
	// name them directly, so they are router settings, not composable objects.
	BogonsV4  string
	BogonsV6  string
	BogonASNs string
	BogonErrs map[string]string

	// RawConfig is the escape hatch, echoed back on a failed save so the
	// operator does not lose what they typed.
	RawConfig string
	RawErr    string
	RawOutput string // what bird -p said, when it rejected the config
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, SettingsView{Active: "settings", ReadOnly: s.readOnly, Msg: r.URL.Query().Get("flash")})
}

func (s *Server) renderSettings(w http.ResponseWriter, v SettingsView) {
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		// A rejected form keeps what the user typed; otherwise show what's stored.
		if len(v.IdentityErrs) == 0 {
			v.Settings = settings
		} else {
			stored := settings
			stored.RouterID = v.Settings.RouterID
			stored.LocalASN = v.Settings.LocalASN
			v.Settings = stored
		}
	}
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
		v := SettingsView{Active: "settings", ReadOnly: s.readOnly, BogonErrs: errs,
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
	http.Redirect(w, r, "/settings?flash="+flash("Bogon lists saved"), http.StatusSeeOther)
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
	routerID := strings.TrimSpace(r.FormValue("routerId"))
	if addr, err := netip.ParseAddr(routerID); err != nil || !addr.Is4() {
		errs["routerId"] = "A BGP router ID is a 32-bit value, written as an IPv4 address."
	}
	asn, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("localAsn")), 10, 64)
	if err != nil || asn < 1 || asn > 4294967295 {
		errs["localAsn"] = "Enter an AS number between 1 and 4294967295."
	}
	probe := store.Settings{RRClusterID: r.FormValue("rrClusterId")}
	if msg := probe.ValidateRRClusterID(); msg != "" {
		errs["rrClusterId"] = msg
	}

	if len(errs) > 0 {
		v := SettingsView{Active: "settings", ReadOnly: s.readOnly, IdentityErrs: errs}
		v.Settings.RouterID = routerID
		v.Settings.LocalASN = sql.NullInt64{Int64: asn, Valid: errs["localAsn"] == ""}
		v.Settings.RRClusterID = probe.RRClusterID
		s.renderSettings(w, v)
		return
	}

	settings.RouterID = routerID
	settings.LocalASN = sql.NullInt64{Int64: asn, Valid: true}
	settings.RRClusterID = probe.RRClusterID
	if err := s.store.SaveSettings(settings); err != nil {
		s.serverError(w, "save settings", err)
		return
	}
	http.Redirect(w, r, "/settings?flash="+flash("Router identity saved"), http.StatusSeeOther)
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
			Active: "settings", ReadOnly: s.readOnly,
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
	http.Redirect(w, r, "/settings?flash="+flash(msg), http.StatusSeeOther)
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
	if s.readOnly {
		http.Error(w, "birdy is running in read-only mode", http.StatusForbidden)
		return
	}
	if s.snap == nil {
		http.Error(w, "snapshots not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("db")
	if err != nil {
		http.Error(w, "missing file field \"db\"", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusBadRequest)
		return
	}
	if err := s.snap.StageRestore(data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{
		"status":  "staged",
		"message": "Restore staged. Restart birdy (systemctl restart birdy) to apply it.",
	})
}
