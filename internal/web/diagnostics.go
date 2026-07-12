package web

import (
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/netdiag"
)

type diagView struct {
	Active   string
	ReadOnly bool
	// Enabled reflects --netdiag: off means the page explains how to turn it on
	// rather than running anything.
	Enabled bool
	Tools   []netdiag.Tool
	Tool    string
	Target  string
	Ran     bool
	Result  netdiag.Result
}

func validDiagTool(t string) bool {
	for _, x := range netdiag.Tools {
		if string(x) == t {
			return true
		}
	}
	return false
}

// handleDiagnostics runs a reachability diagnostic (ping/traceroute) from the
// router. It is a read-only operation — nothing about BIRD or the config
// changes — but it execs external tools, so it does nothing unless --netdiag is
// set. The target is validated to a plain IP or hostname before it reaches a
// command.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	v := diagView{
		Active: "diagnostics", ReadOnly: s.readOnly, Enabled: s.netdiag, Tools: netdiag.Tools,
		Tool: r.URL.Query().Get("tool"), Target: strings.TrimSpace(r.URL.Query().Get("target")),
	}
	if !validDiagTool(v.Tool) {
		v.Tool = string(netdiag.Ping)
	}
	if s.netdiag && v.Target != "" {
		v.Ran = true
		v.Result = netdiag.Run(r.Context(), netdiag.Tool(v.Tool), v.Target)
	}
	render(w, s.log, "diagnostics.html", v)
}
