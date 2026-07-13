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

func (s *Server) buildTimelineView(r *http.Request) TimelineView {
	offset, limit := parsePageParams(r)
	if limit == defaultPageSize {
		limit = timelinePageSize
	}
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

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildTimelineView(r))
}
