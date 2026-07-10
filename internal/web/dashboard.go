package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
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
	Protocols    []protoRow    `json:"protocols"`
	Sessions     []protoRow    `json:"-"`
	Infra        []protoRow    `json:"-"`
	UpCount      int           `json:"upCount"`
	DownCount    int           `json:"downCount"`
	TotalRoutes  int           `json:"totalRoutes"`
	PollErr      string        `json:"pollErr,omitempty"`
	UpdatedAt    time.Time     `json:"updatedAt"`
	RecentEvents []store.Event `json:"recentEvents"`

	// One-line verdict shown in the dashboard hero. Computed here rather than
	// in the template or dashboard.js so the first paint and every poll agree.
	StatusText string `json:"statusText"`
	StatusOK   bool   `json:"statusOK"`
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
	if peers, err := s.store.ListPeers(); err == nil {
		for _, p := range peers {
			configured[p.Name] = true
		}
	}
	for i := range v.Protocols {
		row := &v.Protocols[i]
		if row.IsBGP() {
			row.Configured = configured[row.Name]
			v.Sessions = append(v.Sessions, *row)
		} else {
			v.Infra = append(v.Infra, *row)
		}
	}

	if events, err := s.store.ListEvents(8, 0); err == nil {
		v.RecentEvents = events
	}
	v.StatusText, v.StatusOK = sessionVerdict(v.PollErr, len(v.Protocols), v.DownCount)
	return v
}

// sessionVerdict renders the hero's headline answer to "is my router healthy?".
func sessionVerdict(pollErr string, total, down int) (string, bool) {
	switch {
	case pollErr != "":
		return "BIRD unreachable", false
	case total == 0:
		return "No protocols configured", false
	case down == 0:
		return fmt.Sprintf("All %d %s up", total, plural(total, "session")), true
	default:
		return fmt.Sprintf("%d of %d %s down", down, total, plural(total, "session")), false
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	render(w, s.log, "dashboard.html", s.buildDashboardView())
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildDashboardView())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
