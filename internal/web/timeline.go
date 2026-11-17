package web

import (
	"net/http"

	"github.com/floreabogdan/birdy/internal/store"
)

const timelinePageSize = 50

type TimelineView struct {
	Active   string        `json:"-"`
	ReadOnly bool          `json:"readOnly"`
	Events   []store.Event `json:"events"`
	HasMore  bool          `json:"hasMore"`
	// Pager numbers the pages. The timeline used to walk backwards with a
	// "before this id" cursor, which can only ever go one page at a time — no way
	// to jump to what happened last Tuesday without clicking Next twenty times.
	Pager Pager `json:"-"`
}

// tokenEventsLimit is the most events one instance-token read returns. The
// federation client never asks for more (federation.Client.FetchEvents refuses
// a limit above 50), so nothing a real remote birdy does is affected.
const tokenEventsLimit = 50

func (s *Server) buildTimelineView(r *http.Request) TimelineView {
	offset, limit := parsePageParams(r)
	if limit == defaultPageSize {
		limit = timelinePageSize
	}
	return s.timelinePage(r, offset, limit)
}

// timelinePage is one page of the timeline, offset events in from the newest.
func (s *Server) timelinePage(r *http.Request, offset, limit int) TimelineView {
	v := TimelineView{Active: "timeline", ReadOnly: s.readOnly}

	total, err := s.store.CountEvents()
	if err != nil {
		s.log.Error("count events failed", "error", err)
		return v
	}
	events, err := s.store.ListEventsPage(limit, offset)
	if err != nil {
		s.log.Error("list events failed", "error", err)
		return v
	}
	v.Events = events
	v.Pager = pagerFor(r, offset, limit, len(events), total)
	v.HasMore = v.Pager.HasMore
	return v
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	render(w, s.log, "timeline.html", s.buildTimelineView(r))
}

// apiEvents serves the timeline as JSON, to the browser and to other birdy
// instances. A signed-in operator gets the whole thing, paged as they ask. An
// instance token gets what the fleet view it exists for needs: the newest
// events only — at most tokenEventsLimit, never an offset to page back through
// years of audit trail with — and no actor, so a token that leaks does not
// tell its holder which operator accounts exist and who changed what.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	if !viaToken(r) {
		writeJSON(w, s.buildTimelineView(r))
		return
	}
	_, limit := parsePageParams(r)
	if limit > tokenEventsLimit {
		limit = tokenEventsLimit
	}
	v := s.timelinePage(r, 0, limit)
	// There is no further page for this reader, whatever the table holds.
	v.HasMore = false
	for i := range v.Events {
		v.Events[i].Actor = ""
	}
	writeJSON(w, v)
}
