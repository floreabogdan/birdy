package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// csvSafe neutralizes spreadsheet formula injection. A cell a spreadsheet reads
// as a formula (leading =, +, -, @, tab, or CR) is prefixed with a single quote
// so peer names, BIRD info strings, and event messages cannot execute on open.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte("=+-@\t\r", s[0]) >= 0 {
		return "'" + s
	}
	return s
}

func (s *Server) handleSessionExport(w http.ResponseWriter, r *http.Request) {
	v := s.selectedDashboardView(r)
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="birdy-sessions.json"`)
		writeJSON(w, v.Sessions)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="birdy-sessions.csv"`)
	c := csv.NewWriter(w)
	_ = c.Write([]string{"name", "protocol", "state", "info", "managed", "imported", "exported"})
	for _, row := range v.Sessions {
		managed := "no"
		if row.Configured {
			managed = "yes"
		}
		_ = c.Write([]string{csvSafe(row.Name), csvSafe(row.Proto), csvSafe(row.BGPState()), csvSafe(row.Info), managed, strconv.Itoa(row.Imported), strconv.Itoa(row.Exported)})
	}
	c.Flush()
}

func (s *Server) handleEventExport(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(500, 0)
	if err != nil {
		s.serverError(w, "export events", err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="birdy-events.json"`)
		writeJSON(w, events)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="birdy-events.csv"`)
	c := csv.NewWriter(w)
	_ = c.Write([]string{"id", "timestamp", "kind", "protocol", "actor", "message"})
	for _, event := range events {
		_ = c.Write([]string{strconv.FormatInt(event.ID, 10), event.Ts.Format(time.RFC3339Nano), csvSafe(event.Kind), csvSafe(event.Protocol), csvSafe(event.Actor), csvSafe(event.Message)})
	}
	c.Flush()
}
