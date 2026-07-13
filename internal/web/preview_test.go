package web

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

// nopHeaderWriter lets render() write into a buffer: it only needs Header()
// and Write(), and the headers it sets here are discarded.
type nopHeaderWriter struct{ buf *bytes.Buffer }

func (n *nopHeaderWriter) Header() http.Header         { return http.Header{} }
func (n *nopHeaderWriter) Write(b []byte) (int, error) { return n.buf.Write(b) }
func (n *nopHeaderWriter) WriteHeader(int)             {}

// TestPreview renders pages with representative data and screenshots them with
// headless Chrome. Design-review scaffold, skipped unless BIRDY_PREVIEW is set.
func TestPreview(t *testing.T) {
	chrome := os.Getenv("BIRDY_PREVIEW")
	if chrome == "" {
		t.Skip("set BIRDY_PREVIEW=<path to chrome.exe> to render screenshots")
	}
	out := os.Getenv("BIRDY_PREVIEW_OUT")
	if out == "" {
		t.Fatal("set BIRDY_PREVIEW_OUT to a directory")
	}

	now := time.Now()
	dash := DashboardView{
		Active:      "dashboard",
		ReadOnly:    true,
		Status:      birdc.Status{Version: "2.17.1", RouterID: "192.0.2.1", Hostname: "rtr1.example.net"},
		LocalASN:    "65551",
		TotalRoutes: 2600141,
		UpCount:     3,
		DownCount:   1,
		UpdatedAt:   now,
		Protocols: []protoRow{
			{Name: "edge_v4", Proto: "BGP", Table: "master4", State: "Established", Since: "2026-07-08 11:02:31", Up: true,
				HasCounts: true, Imported: 984213, Exported: 12, LimitPct: 98.4, LimitText: "984,213 / 1,000,000 (ipv4)"},
			{Name: "edge_v6", Proto: "BGP", Table: "master6", State: "Established", Since: "2026-07-08 11:02:33", Up: true,
				HasCounts: true, Imported: 191204, Exported: 8, LimitPct: 76.4, LimitText: "191,204 / 250,000 (ipv6)"},
			{Name: "ibgp_core", Proto: "BGP", Table: "master4", State: "Established", Since: "2026-07-09 04:19:00", Up: true,
				HasCounts: true, Imported: 42, Exported: 900, LimitPct: -1},
			{Name: "device1", Proto: "Device", Table: "---", State: "Idle", Since: "2026-07-08 11:02:29", Up: false, LimitPct: -1, Info: "down"},
		},
		RecentEvents: []store.Event{
			{Ts: now.Add(-90 * time.Second), Kind: store.EventLimitHit, Protocol: "edge_v4", Message: "import limit 98% of 1000000"},
			{Ts: now.Add(-42 * time.Minute), Kind: store.EventSessionUp, Protocol: "ibgp_core", Message: "session established"},
			{Ts: now.Add(-3 * time.Hour), Kind: store.EventFlap, Protocol: "edge_v6", Message: "3 transitions in 5m"},
		},
	}
	for i := range dash.Protocols {
		row := dash.Protocols[i]
		if row.IsBGP() {
			row.Configured = row.Name != "edge_v6"
			dash.Sessions = append(dash.Sessions, row)
			if row.Up {
				dash.SessionUp++
			} else {
				dash.SessionDown++
			}
		} else {
			dash.Infra = append(dash.Infra, row)
		}
	}
	dash.StatusText, dash.StatusOK = sessionVerdict(dash.PollErr, len(dash.Sessions), dash.SessionDown)

	peer := SessionDetailView{
		Active:     "peers",
		Tab:        "general",
		Name:       "edge_v4",
		Configured: true,
		Detail: birdc.ProtocolDetail{
			Summary:         birdc.ProtocolSummary{Proto: "BGP", State: "up"},
			BGPState:        "Established",
			NeighborAddress: "198.51.100.1",
			NeighborAS:      "64497",
			LocalAS:         "65551",
			NeighborID:      "198.51.100.1",
			SessionType:     "External AS4",
			SourceAddress:   "192.0.2.2",
			HoldTimer:       "142.7/180",
			KeepaliveTimer:  "23.1/60",
			Channels: []birdc.ChannelDetail{
				{AFI: "ipv4", State: "UP", Table: "master4", Preference: "100", ImportFilter: "ACCEPT", ExportFilter: "REJECT",
					ImportLimit: "1000000", ImportLimitAction: "restart", RoutesImported: 984213, RoutesExported: 12, RoutesPreferred: 900112},
			},
			RawLines: []string{"edge_v4    BGP    ---    up    2026-07-08 11:02:31  Established"},
		},
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// ?dark stamps data-theme on <html> the way theme.js does at runtime, so
	// the dark palette can be screenshotted without driving devtools.
	page := func(name string, data any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var buf bytes.Buffer
			render(&nopHeaderWriter{&buf}, log, name, data)
			html := buf.String()
			if r.URL.Query().Has("dark") {
				html = strings.Replace(html, "<html>", `<html data-theme="dark">`, 1)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, html)
		}
	}
	announce := store.PrefixSet{
		ID: 3, Name: "ANNOUNCE_V4", Description: "Our aggregates", Family: store.FamilyV4, Originate: true,
		Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24"}},
	}
	bogons := store.PrefixSet{
		ID: 1, Name: store.BogonSetV4, Description: "IPv4 martians and special-use space (RFC 6890).",
		Family: store.FamilyV4, Builtin: true, System: true,
		Entries: []store.PrefixEntry{{Prefix: "10.0.0.0/8", Modifier: "+"}, {Prefix: "192.168.0.0/16", Modifier: "+"}},
	}
	bogonsV6 := store.PrefixSet{
		ID: 2, Name: store.BogonSetV6, Description: "IPv6 martians and special-use space (RFC 6890).",
		Family: store.FamilyV6, Builtin: true, System: true,
		Entries: []store.PrefixEntry{{Prefix: "fc00::/7", Modifier: "+"}},
	}
	impSanity := store.Policy{ID: 100, Name: "IMPORT_SANITY", Direction: store.DirImport,
		Description: "Standard eBGP hygiene.", DefaultRoute: store.DefaultReject, RejectBogonPrefixes: true,
		MinLenV4: 8, MaxLenV4: 24, MinLenV6: 12, MaxLenV6: 48,
		RejectOwnASN: true, MaxASPathLen: 64, BogonASNs: store.BogonASNsAll, Builtin: true}
	impDefaultOnly := store.Policy{ID: 101, Name: "IMPORT_DEFAULT_ONLY", Direction: store.DirImport,
		Description: "Accept nothing but the default route.", DefaultRoute: store.DefaultOnly,
		BogonASNs: store.BogonASNsOff, Builtin: true}
	expOwn := store.Policy{ID: 110, Name: "EXPORT_OWN_AND_CUSTOMERS", Direction: store.DirExport,
		Description:          "Our aggregates plus everything our customers send us.",
		AnnounceFromCustomer: true, RejectBogonPrefixes: true, SetIDs: []int64{3}, Builtin: true}
	expDownstream := store.Policy{ID: 111, Name: "EXPORT_DOWNSTREAM", Direction: store.DirExport,
		AnnounceDefault: true, AnnounceFromIX: true, AnnounceFromCustomer: true,
		RejectBogonPrefixes: true, SetIDs: []int64{3}, Builtin: true}
	allPolicies := []store.Policy{impSanity, impDefaultOnly, expOwn, expDownstream}

	modelPeer := store.Peer{
		ID: 1, Name: "edge_v4", Description: "Example Transit", Role: store.RoleUpstream, Enabled: true,
		EnforceFirstAS: true, NeighborIP: "198.51.100.1", RemoteASN: 64496, LocalIP: "192.0.2.2",
		Password: "hunter2", ImportLimit: 1000000, ImportLimitAction: "restart",
		ImportPolicies: []store.Policy{impSanity}, ExportPolicies: []store.Policy{expOwn},
	}
	customer := store.Peer{
		ID: 2, Name: "cust_a", Description: "downstream", Role: store.RoleCustomer, Enabled: true,
		EnforceFirstAS: true, NeighborIP: "198.51.100.13", RemoteASN: 64600, ImportLimitAction: "restart",
		ImportPolicies: []store.Policy{impSanity}, ExportPolicies: []store.Policy{expDownstream},
	}
	disabled := store.Peer{
		ID: 3, Name: "ibgp_core", Role: store.RoleIBGP, NeighborIP: "10.0.0.2", RemoteASN: 65551,
		Multihop: 2, ImportLimitAction: "restart",
	}

	peersV := peersView{Active: "peers", Peers: []store.Peer{modelPeer, customer, disabled}, Flash: "Saved edge_v4",
		Live: map[string]protoRow{
			"edge_v4": {Name: "edge_v4", Up: true, State: "up", Info: "Established",
				HasCounts: true, Imported: 984213, Filtered: 118, Exported: 12},
			"ibgp_core": {Name: "ibgp_core", Up: false, State: "start", Info: "Active"},
		}}

	rpkiV := rpkiView{Active: "rpki",
		Servers: []store.RPKIServer{
			{ID: 1, Name: "rpki_local", Description: "Routinator on this box", Host: "127.0.0.1", Port: 3323,
				Enabled: true, Refresh: 900, Retry: 90, Expire: 172800},
			{ID: 2, Name: "cloudflare", Description: "Public fallback", Host: "rtr.rpki.cloudflare.com", Port: 8282,
				Enabled: false},
		},
		Live:    map[string]protoRow{"rpki_local": {Name: "rpki_local", Up: true, State: "up", Info: "Established"}},
		Logging: []string{"IMPORT_SANITY"},
	}

	sets := []store.PrefixSet{bogons, bogonsV6, announce}
	formV := peerFormView{Active: "peers", Peer: modelPeer}
	for _, p := range allPolicies {
		if p.IsImport() {
			formV.Imports = append(formV.Imports, p)
		} else {
			formV.Exports = append(formV.Exports, p)
		}
	}
	formV.Preview, formV.PreviewErr, formV.Warnings = previewPeer(formV.Peer, sets, nil, allPolicies, nil, nil, 65551, "")

	// A peer whose configuration parses but misbehaves, to show the lint panel.
	leaky := customer
	leaky.Name, leaky.Role, leaky.RemoteASN = "leaky", store.RoleUpstream, 65001
	expFull := store.Policy{ID: 112, Name: "EXPORT_FULL_TABLE", Direction: store.DirExport, AnnounceEverything: true, Builtin: true}
	leaky.ExportPolicies = []store.Policy{expFull}
	leakyV := peerFormView{Active: "peers", Peer: leaky, Imports: formV.Imports,
		Exports: append(append([]store.Policy{}, formV.Exports...), expFull)}
	leakyV.Preview, leakyV.PreviewErr, leakyV.Warnings = previewPeer(leaky, sets, nil, append(allPolicies, expFull), nil, nil, 65551, "")

	policiesV := policiesView{Active: "policies", Imports: formV.Imports, Exports: formV.Exports,
		InUse: map[int64]int{100: 2, 110: 1}, SetNames: map[int64]string{3: "ANNOUNCE_V4", 1: "BOGONS_V4"}}

	polFormV := policyFormView{Active: "policies", Policy: impSanity, Sets: []store.PrefixSet{announce}}
	polFormV.Preview, polFormV.PreviewErr = previewPolicy(impSanity, sets, nil, nil, nil, 65551)
	expFormV := policyFormView{Active: "policies", Policy: expDownstream, Sets: []store.PrefixSet{announce}}
	expFormV.Preview, expFormV.PreviewErr = previewPolicy(expDownstream, sets, nil, nil, nil, 65551)

	setsV := prefixSetsView{Active: "library", Sets: []store.PrefixSet{announce}, InUse: map[int64]int{3: 1}}

	setFormV := prefixSetFormView{Active: "library", Set: announce}
	setFormV.Preview, setFormV.PreviewErr = previewPrefixSet(setFormV.Set)

	// A changes view with a real diff: pretend the running config is an older
	// render that lacked the export filter.
	changesIn := birdconf.Input{
		RouterID: "192.0.2.1", LocalASN: 65551, PrefixSets: sets, Policies: allPolicies,
		Peers: []store.Peer{modelPeer, customer, disabled}, MaskSecrets: true,
		Version: "0.3.0", Generated: now,
	}
	candidate, err := birdconf.Config(changesIn)
	if err != nil {
		t.Fatal(err)
	}
	oldCfg := strings.Replace(candidate, "\t\texport filter ebgp_out_edge_v4;\n", "\t\texport all;\n", 1)
	oldCfg = strings.Replace(oldCfg, "\timport limit 1000000 action restart;", "\timport limit 500000 action warn;", 1)
	hunks := birdconf.Diff(oldCfg, candidate, 3)
	added, removed := birdconf.Stat(hunks)
	changesV := changesView{
		Active: "changes", Tab: "config", Candidate: candidate,
		CandidateLines: strings.Split(strings.TrimSuffix(candidate, "\n"), "\n"),
		LivePath:       "/etc/bird/bird.conf",
		Hunks:          hunks, Added: added, Removed: removed, Warnings: birdconf.Lint(changesIn),
		Check:     birdconf.CheckResult{OK: true, Output: "Configuration OK"},
		PeerCount: 3, SetCount: 3, PolicyCount: 4,
	}

	settingsV := SettingsView{
		Active: "settings", ReadOnly: true, Msg: "Router identity saved",
		Settings: store.Settings{
			RouterLabel: "cl1", RouterID: "192.0.2.1",
			LocalASN:       sql.NullInt64{Int64: 65551, Valid: true},
			BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "0.0.0.0:8080",
		},
		LatestSnapshot: "/var/lib/birdy/snapshots/2026-07-10.db",
		BogonsV4:       defaultBogonText(store.FamilyV4),
		BogonsV6:       defaultBogonText(store.FamilyV6),
		BogonASNs:      store.FormatBogonASNs(store.DefaultBogonASNs()),
	}
	timelineV := TimelineView{Active: "timeline", Events: dash.RecentEvents, NextID: 42}

	// Looking glass with "show all" attributes: decoded communities of every kind.
	lgV := LGView{
		Active: "lg", ReadOnly: true, Type: "for", Target: "203.0.113.0/24", All: true, Ran: true,
		FirstRow: 1, LastRow: 2,
		Tables: []lgTable{{
			Name: "master4",
			Routes: []lgRoute{
				{RouteEntry: birdc.RouteEntry{
					Network: "203.0.113.0/24", Type: "unicast", Protocol: "edge_v4", Since: "2026-07-08",
					Primary: true, Preference: 100, ASPath: "64496 64500", NextHop: "via 198.51.100.1 on eno1",
					LocalPref: "100", Origin: "IGP", MED: "0",
				}, Comms: []commChip{
					{Text: "(65000, 100)", Name: "CUSTOMER_EU", Kind: "named"},
					{Text: "(65535, 666)", Name: "BLACKHOLE", Kind: "wellknown"},
					{Text: "(65551, 1, 3000)", Name: "FROM_CUSTOMER", Kind: "origin"},
					{Text: "(65551, 2, 1)", Name: "RPKI_INVALID", Kind: "rpki"},
					{Text: "(65001, 7)"},
				}},
				{RouteEntry: birdc.RouteEntry{
					Network: "203.0.113.0/24", Type: "unicast", Protocol: "ibgp_core", Since: "2026-07-09",
					Preference: 100, ASPath: "64496 64500", NextHop: "via 10.0.0.2 on eno2", From: "10.0.0.2",
					LocalPref: "100", Origin: "IGP",
				}, Comms: []commChip{
					{Text: "(65551, 1, 2000)", Name: "FROM_IX", Kind: "origin"},
				}},
			},
		}},
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler()))
	mux.HandleFunc("/", page("dashboard.html", dash))
	mux.HandleFunc("/peer", page("peer_detail.html", peer))
	mux.HandleFunc("/settings", page("settings.html", settingsV))
	mux.HandleFunc("/timeline", page("timeline.html", timelineV))
	mux.HandleFunc("/peers", page("peers.html", peersV))
	mux.HandleFunc("/peer-form", page("peer_form.html", formV))
	mux.HandleFunc("/prefix-sets", page("prefix_sets.html", setsV))
	mux.HandleFunc("/prefix-set-form", page("prefix_set_form.html", setFormV))
	mux.HandleFunc("/changes", page("changes.html", changesV))
	mux.HandleFunc("/rpki", page("rpki.html", rpkiV))
	mux.HandleFunc("/policies", page("policies.html", policiesV))
	mux.HandleFunc("/policy-form", page("policy_form.html", polFormV))
	mux.HandleFunc("/export-policy-form", page("policy_form.html", expFormV))
	mux.HandleFunc("/peer-form-lint", page("peer_form.html", leakyV))
	mux.HandleFunc("/lg", page("lg.html", lgV))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	shots := []struct{ name, path, size string }{
		{"dashboard", "/", "1600,1500"},
		{"dashboard-narrow", "/", "820,1500"},
		{"peer", "/peer", "1600,1400"},
		{"dashboard-dark", "/?dark", "1600,1500"},
		{"peers", "/peers", "1600,900"},
		{"peer-form", "/peer-form", "1600,1500"},
		{"prefix-sets", "/prefix-sets", "1600,900"},
		{"prefix-set-form", "/prefix-set-form", "1600,1100"},
		{"changes", "/changes", "1600,1500"},
		{"changes-dark", "/changes?dark", "1600,1500"},
		{"policies", "/policies", "1600,1000"},
		{"rpki", "/rpki", "1600,1100"},
		{"policy-form", "/policy-form", "1600,1400"},
		{"export-policy-form", "/export-policy-form", "1600,1200"},
		{"peer-form-lint", "/peer-form-lint", "1600,1500"},
		{"settings", "/settings", "1600,1100"},
		{"timeline", "/timeline", "1600,700"},
		{"lg", "/lg", "1600,700"},
		{"lg-dark", "/lg?dark", "1600,700"},
	}
	for _, s := range shots {
		png := out + "/" + s.name + ".png"
		cmd := exec.Command(chrome, "--headless", "--disable-gpu", "--hide-scrollbars",
			"--virtual-time-budget=1500", "--screenshot="+png, "--window-size="+s.size, srv.URL+s.path)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("chrome %s: %v\n%s", s.name, err, b)
		}
		t.Logf("wrote %s", png)
	}
}
