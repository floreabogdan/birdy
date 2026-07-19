package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/federation"
	"github.com/floreabogdan/birdy/internal/poller"
	"github.com/floreabogdan/birdy/internal/store"
)

type protoRow struct {
	Name  string `json:"name"`
	Proto string `json:"proto"`
	Table string `json:"table"`
	State string `json:"state"`
	Since string `json:"since"`
	Info  string `json:"info"`
	Up    bool   `json:"up"`

	// Route counts summed across the session's channels. HasCounts is
	// false when no detail is available: non-BGP protocols and sessions
	// that are down.
	HasCounts bool    `json:"hasCounts"`
	Imported  int     `json:"imported"`
	Filtered  int     `json:"filtered"`
	Exported  int     `json:"exported"`
	LimitPct  float64 `json:"limitPct"`  // worst channel, 0–100; -1 when no import limit is set
	LimitText string  `json:"limitText"` // e.g. "137 / 1000 (ipv4)"

	// Configured reports whether birdy's model has a peer of this name. Only
	// meaningful for BGP: device and kernel protocols are birdy's own scaffolding.
	Configured bool `json:"configured"`
	// Disabled: the model has this peer switched off, so BIRD is not even trying
	// to connect. BIRD calls that "down", the same word it uses for a session that
	// failed — but one is a fault and the other is a decision, and a dashboard that
	// paints them the same colour teaches you to ignore red.
	Disabled bool `json:"disabled"`
}

// IsBGP separates the sessions an operator cares about from the plumbing BIRD
// needs to function (device, kernel, static, direct).
func (p protoRow) IsBGP() bool { return strings.EqualFold(p.Proto, "BGP") }

// BGPState is the state BIRD itself names — Established, Active, Connect, Idle —
// which "show protocols" reports in the Info column. State is only up/down/start,
// and an operator reading a BGP table expects BIRD's vocabulary.
func (p protoRow) BGPState() string {
	if p.Info != "" {
		return p.Info
	}
	return p.State
}

// DashboardView is both the html/template data for GET / and the JSON body
// for GET /api/dashboard, so the two can never drift apart.
type DashboardView struct {
	Active   string       `json:"-"`
	ReadOnly bool         `json:"readOnly"`
	Status   birdc.Status `json:"status"`
	LocalASN string       `json:"localASN"`
	// Protocols is every protocol BIRD reports; Sessions and Infra are the same
	// rows split for display. The API keeps the flat list.
	Protocols []protoRow `json:"protocols"`
	Sessions  []protoRow `json:"-"`
	Infra     []protoRow `json:"-"`
	// UpCount/DownCount count every protocol; SessionUp/SessionDown count only the
	// BGP sessions. The dashboard's session stats and health verdict use the latter
	// — device/kernel/static/RPKI are infrastructure, not sessions.
	UpCount   int `json:"upCount"`
	DownCount int `json:"downCount"`
	SessionUp int `json:"sessionUp"`
	// SessionDown counts only sessions that are down and were meant to be up.
	// SessionDisabled counts the ones switched off in the model — they are not a
	// fault, and folding them into "down" would make the health verdict lie.
	SessionDown      int           `json:"sessionDown"`
	SessionDisabled  int           `json:"sessionDisabled"`
	SessionManaged   int           `json:"sessionManaged"`
	SessionUnmanaged int           `json:"sessionUnmanaged"`
	TotalRoutes      int           `json:"totalRoutes"`
	PollErr          string        `json:"pollErr,omitempty"`
	UpdatedAt        time.Time     `json:"updatedAt"`
	RecentEvents     []store.Event `json:"recentEvents"`

	// One-line verdict shown in the dashboard hero. Computed here rather than
	// in the template or dashboard.js so the first paint and every poll agree.
	StatusText string `json:"statusText"`
	StatusOK   bool   `json:"statusOK"`

	// History is a short, downsampled imported-route series per session name, for
	// the dashboard's trend sparklines. Each point carries its timestamp so the
	// chart can name what is under the cursor. Sent in the JSON so the sparkline
	// survives the live table rebuild, and drawn identically server-side on first
	// paint.
	History        map[string]Series `json:"history"`
	InstanceName   string            `json:"instanceName"`
	InstanceRemote bool              `json:"instanceRemote"`
}

// buildProtoRows turns a poll snapshot into the table rows shared by the
// dashboard and the sessions page, so the two can never disagree about what
// BIRD is doing.
func buildProtoRows(snap poller.Snapshot) (rows []protoRow, up, down int) {
	for _, p := range snap.Protocols {
		row := protoRow{
			Name: p.Name, Proto: p.Proto, Table: p.Table, State: p.State, Since: p.Since, Info: p.Info,
			LimitPct: -1,
		}
		st, ok := snap.States[p.Name]
		if ok {
			row.Up = st.Up
		}
		if row.Up {
			up++
		} else {
			down++
		}
		if ok && len(st.Channels) > 0 {
			row.HasCounts = true
			for _, ch := range st.Channels {
				row.Imported += ch.RoutesImported
				row.Filtered += ch.RoutesFiltered
				row.Exported += ch.RoutesExported
				limit, err := strconv.Atoi(strings.TrimSpace(ch.ImportLimit))
				if err != nil || limit <= 0 {
					continue
				}
				pct := min(float64(ch.RoutesImported)/float64(limit)*100, 100)
				if pct >= row.LimitPct {
					row.LimitPct = pct
					row.LimitText = fmt.Sprintf("%s / %s (%s)", comma(ch.RoutesImported), comma(limit), ch.AFI)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, up, down
}

func (s *Server) buildDashboardView() DashboardView {
	snap := s.poller.Snapshot()
	v := DashboardView{
		Active:      "dashboard",
		ReadOnly:    s.readOnly,
		Status:      snap.Status,
		UpdatedAt:   snap.UpdatedAt,
		TotalRoutes: snap.TotalRoutes,
	}
	if settings, ok, err := s.store.GetSettings(); err == nil && ok && settings.LocalASN.Valid {
		v.LocalASN = strconv.FormatInt(settings.LocalASN.Int64, 10)
	}
	if snap.Err != nil {
		v.PollErr = snap.Err.Error()
	}
	v.Protocols, v.UpCount, v.DownCount = buildProtoRows(snap)

	// Annotate BGP rows with whether birdy manages them, and split the plumbing
	// out of the main table.
	configured := map[string]bool{}
	disabled := map[string]bool{}
	if enabledByName, err := s.store.PeerNameEnabled(); err == nil {
		for name, enabled := range enabledByName {
			configured[name] = true
			disabled[name] = !enabled
		}
	}
	for i := range v.Protocols {
		row := &v.Protocols[i]
		if row.IsBGP() {
			row.Configured = configured[row.Name]
			row.Disabled = disabled[row.Name]
			v.Sessions = append(v.Sessions, *row)
			if row.Configured {
				v.SessionManaged++
			} else {
				v.SessionUnmanaged++
			}
			switch {
			// A disabled peer that BIRD still has up has not been applied yet; it is
			// genuinely still carrying traffic, so it counts as up until it isn't.
			case row.Disabled && !row.Up:
				v.SessionDisabled++
			case row.Up:
				v.SessionUp++
			default:
				v.SessionDown++
			}
		} else {
			v.Infra = append(v.Infra, *row)
		}
	}

	if events, err := s.store.ListEvents(8, 0); err == nil {
		v.RecentEvents = events
	}
	v.History = s.dashboardHistory()
	v.StatusText, v.StatusOK = sessionVerdict(v.PollErr, v.SessionUp+v.SessionDown, v.SessionDown, v.SessionDisabled)
	return v
}

// sessionVerdict renders the hero's headline answer to "is my router healthy?".
// total counts only the sessions meant to be up: a peer you switched off is not a
// session that is down, and rolling it into the verdict would turn a deliberate
// change into a permanent red banner.
func sessionVerdict(pollErr string, total, down, disabled int) (string, bool) {
	off := ""
	if disabled > 0 {
		off = fmt.Sprintf(" · %d disabled", disabled)
	}
	switch {
	case pollErr != "":
		return "BIRD unreachable", false
	case total == 0 && disabled > 0:
		return fmt.Sprintf("No active BGP sessions (%d disabled)", disabled), true
	case total == 0:
		return "No BGP sessions", false
	case down == 0:
		return fmt.Sprintf("All %d %s up%s", total, plural(total, "session"), off), true
	default:
		return fmt.Sprintf("%d of %d %s down%s", down, total, plural(total, "session"), off), false
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	render(w, s.log, "dashboard.html", s.selectedDashboardView(r))
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.selectedDashboardView(r))
}

func (s *Server) selectedDashboardView(r *http.Request) DashboardView {
	id := selectedInstanceID(r)
	if id == 0 {
		v := s.buildDashboardView()
		v.InstanceName = s.localInstanceName()
		return v
	}
	instance, ok, err := s.store.GetInstance(id)
	if err != nil || !ok {
		v := DashboardView{Active: "dashboard", InstanceName: "Unknown instance", InstanceRemote: true, PollErr: "The selected Birdy instance no longer exists."}
		v.StatusText, v.StatusOK = sessionVerdict(v.PollErr, 0, 0, 0)
		return v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	body, err := (federation.Client{BaseURL: instance.BaseURL, Token: instance.Token}).FetchDashboard(ctx)
	if err != nil {
		v := DashboardView{Active: "dashboard", InstanceName: instance.Name, InstanceRemote: true, PollErr: err.Error()}
		v.StatusText, v.StatusOK = sessionVerdict(v.PollErr, 0, 0, 0)
		return v
	}
	var v DashboardView
	if err := json.Unmarshal(body, &v); err != nil {
		v.PollErr = "The remote Birdy returned invalid dashboard data."
	}
	v.Active, v.InstanceName, v.InstanceRemote = "dashboard", instance.Name, true
	if v.PollErr != "" {
		v.StatusText, v.StatusOK = sessionVerdict(v.PollErr, 0, 0, 0)
	}
	for _, row := range v.Protocols {
		if row.IsBGP() {
			v.Sessions = append(v.Sessions, row)
		} else {
			v.Infra = append(v.Infra, row)
		}
	}
	return v
}

// dashboardHistory returns the per-session trend series, recomputing it at most
// once per dashboardHistoryTTL. seriesByProtocol builds a fresh map each time
// and the pointer is swapped under histMu, so readers holding an earlier map are
// never mutated underneath them. On a read error the last good series is served.
func (s *Server) dashboardHistory() map[string]Series {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	if s.histCache != nil && time.Since(s.histComputed) < dashboardHistoryTTL {
		return s.histCache
	}
	samples, err := s.store.RecentSamples(time.Now().Add(-dashboardHistoryWindow))
	if err != nil {
		return s.histCache
	}
	s.histCache = seriesByProtocol(samples, dashboardHistoryPoints)
	s.histComputed = time.Now()
	return s.histCache
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
