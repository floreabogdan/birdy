package web

import (
	"net/http"
	"strconv"

	"github.com/floreabogdan/birdy/internal/store"
)

const timelinePageSize = 50

type TimelineView struct {
	Active   string        `json:"-"`
	ReadOnly bool          `json:"readOnly"`
	Events   []store.Event `json:"events"`
	NextID   int64         `json:"nextId,omitempty"`
	HasMore  bool          `json:"hasMore"`
}

func (s *Server) buildTimelineView(r *http.Request) TimelineView {
	var before int64
	if b := r.URL.Query().Get("before"); b != "" {
		before, _ = strconv.ParseInt(b, 10, 64)
	}
	v := TimelineView{Active: "timeline", ReadOnly: s.readOnly}
	events, err := s.store.ListEvents(timelinePageSize, before)
	if err != nil {
		s.log.Error("list events failed", "error", err)
		return v
	}
	v.Events = events
	if len(events) == timelinePageSize {
		v.NextID = events[len(events)-1].ID
		v.HasMore = true
	}
	return v
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	render(w, s.log, "timeline.html", s.buildTimelineView(r))
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildTimelineView(r))
}
