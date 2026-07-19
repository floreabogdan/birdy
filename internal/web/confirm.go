package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// confirmSpec describes a destructive action's server-rendered confirmation.
// Post is built from a whitelisted template and a validated path parameter —
// never from an untrusted URL — so /confirm can't be turned into an open
// POST-redirect. This backs the no-JS path; with JS, the danger link is
// intercepted for an inline confirm-and-POST (see ui.js).
type confirmSpec struct {
	title   string
	message string // when needsTarget, a single %s is replaced with the target label
	post    string // when needsTarget, a single %s is replaced with the escaped target
	label   string // confirm button text
	cancel  string // where the Cancel link returns to
	// needsTarget is true for actions scoped to a named/identified object
	// (delete peer X); false for whole-instance actions (adopt, revoke legacy).
	needsTarget bool
}

var confirmSpecs = map[string]confirmSpec{
	"peer-delete": {
		title: "Delete peer", message: "Delete peer %s? This only changes birdy's model — the running BIRD session is untouched until you apply.",
		post: "/peers/%s/delete", label: "Delete peer", cancel: "/peers", needsTarget: true,
	},
	"alert-delete": {
		title: "Delete alert destination", message: "Delete alert destination %s?",
		post: "/alerts/%s/delete", label: "Delete", cancel: "/alerts", needsTarget: true,
	},
	"instance-delete": {
		title: "Remove remote instance", message: "Remove remote instance %s? Its dashboard tile and health history are dropped; the remote Birdy is untouched.",
		post: "/instances/%s/delete", label: "Remove", cancel: "/instances", needsTarget: true,
	},
	"instance-token-revoke": {
		title: "Revoke dashboard token", message: "Revoke this remote dashboard token? Any observer using it loses access immediately.",
		post: "/settings/instance-token/%s/revoke", label: "Revoke", cancel: "/settings", needsTarget: true,
	},
	"instance-token-revoke-all": {
		title: "Revoke legacy token", message: "Revoke the legacy remote dashboard token now? Any observer still using it loses access immediately.",
		post: "/settings/instance-token/revoke", label: "Revoke", cancel: "/settings",
	},
	"adopt": {
		title: "Adopt this config", message: "Back up the current bird.conf and let birdy manage it from now on? A hand-managed file is preserved as a backup first.",
		post: "/apply/adopt", label: "Adopt config", cancel: "/changes",
	},
}

type confirmView struct {
	Active   string
	ReadOnly bool
	Title    string
	Message  string
	Post     string
	Label    string
	Cancel   string
}

// handleConfirm renders the no-JS confirmation interstitial for a destructive
// action. The action is a whitelisted key, and any target is validated before
// it is placed in the POST path, so this handler cannot be coerced into posting
// to an arbitrary URL. The POST it renders is still gated by auth, read-only,
// and the same-origin write check like any other.
func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	spec, ok := confirmSpecs[r.URL.Query().Get("do")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	post, message := spec.post, spec.message
	if spec.needsTarget {
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		if target == "" || strings.ContainsAny(target, "/\\\r\n") {
			http.Error(w, "invalid confirmation target", http.StatusBadRequest)
			return
		}
		post = fmt.Sprintf(spec.post, url.PathEscape(target))
		message = fmt.Sprintf(spec.message, target)
	}
	render(w, s.log, "confirm.html", confirmView{
		ReadOnly: s.readOnly, Title: spec.title, Message: message, Post: post, Label: spec.label, Cancel: spec.cancel,
	})
}
