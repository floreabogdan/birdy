package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// An RTR server called "all" turns the restart button into BIRD's "restart
// all", which bounces every session on the router. Such a row can predate the
// name check or come from a restored database, so the handler does not rely on
// the name being refused at save: it restarts only what the last poll saw
// running as an RPKI protocol under exactly that name.
func TestRPKIRestartOnlyForALiveRPKIProtocol(t *testing.T) {
	env := newTestEnv(t, false)
	withLiveFeed(t, env) // "cf", live, RPKI
	for _, name := range []string{
		"all",       // BIRD's keyword
		"edge_v4",   // a live protocol, but a BGP session
		"unapplied", // in the model, not in BIRD yet
	} {
		if _, err := env.store.CreateRPKIServer(store.RPKIServer{Name: name, Host: "192.0.2.53", Port: 323, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		rec := env.do(t, "POST", "/rpki/"+name+"/restart", url.Values{})
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: code %d, want a redirect with a flash", name, rec.Code)
		}
		if len(env.fc.restarts) != 0 {
			t.Fatalf("%s: birdy sent restart %q", name, env.fc.restarts)
		}
		if loc := flashOf(rec); !strings.Contains(loc, "not running") {
			t.Errorf("%s: the operator should be told why, flash=%q", name, loc)
		}
	}
	// A refused restart is not an operator action on the router: no audit entry.
	events, err := env.store.ListEvents(20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == store.EventRPKIRestart {
			t.Errorf("a refused restart was audited: %+v", e)
		}
	}
	// The live feed still restarts.
	env.do(t, "POST", "/rpki/cf/restart", url.Values{})
	if len(env.fc.restarts) != 1 || env.fc.restarts[0] != "cf" {
		t.Fatalf("restarts = %q, want [cf]", env.fc.restarts)
	}
}

func rpkiForm(name string) url.Values {
	return url.Values{"name": {name}, "host": {"192.0.2.53"}, "port": {"323"}, "enabled": {"on"}}
}

// New and renamed objects cannot take a keyword as their name.
func TestKeywordNameRefusedOnCreateAndRename(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "POST", "/rpki/new", rpkiForm("all"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "command line") {
		t.Errorf("creating an RTR server named all: code=%d, want the form back with the keyword error", rec.Code)
	}
	if _, err := env.store.GetRPKIServerByName("all"); err != store.ErrNotFound {
		t.Errorf("an RTR server named all was created (err %v)", err)
	}

	if rec := env.do(t, "POST", "/rpki/new", rpkiForm("rtr1")); rec.Code != http.StatusSeeOther {
		t.Fatalf("create rtr1: %d", rec.Code)
	}
	if rec := env.do(t, "POST", "/rpki/rtr1/edit", rpkiForm("status")); rec.Code != http.StatusOK {
		t.Errorf("renaming to a keyword: code=%d, want the form back", rec.Code)
	}
	if _, err := env.store.GetRPKIServerByName("rtr1"); err != nil {
		t.Errorf("rtr1 should be unchanged: %v", err)
	}
}

// An object that already carries such a name — saved by an older birdy — must
// not be stuck: it can be edited as it is, renamed away, and deleted.
func TestExistingKeywordNameStaysEditable(t *testing.T) {
	env := applyReady(t)

	// One object of every kind whose name renders as a bare symbol, created
	// under an ordinary name and then renamed behind the store's back.
	kinds := []struct {
		table, base string
		form        url.Values
	}{
		{"rpki_servers", "/rpki", rpkiForm("x1")},
		{"bmp_stations", "/bmp", url.Values{"name": {"x2"}, "address": {"198.51.100.10"}, "port": {"1790"}, "monitorPre": {"on"}, "enabled": {"on"}}},
		{"prefix_sets", "/library/prefix-sets", url.Values{"name": {"x3"}, "family": {"ipv4"}, "entries": {"192.0.2.0/24"}}},
		{"as_sets", "/library/as-sets", url.Values{"name": {"x4"}, "entries": {"64500"}}},
		{"communities", "/library/communities", url.Values{"name": {"x5"}, "value": {"65000:100"}}},
		{"peer_templates", "/peers/templates", templateForm(env, t)},
		{"peers", "/peers", peerForm()},
	}
	// Each kind gets its own keyword: BIRD symbols share one namespace, and
	// birdy refuses a name another kind already uses.
	words := []string{"all", "status", "show", "route", "reload", "restart", "check"}
	for i, k := range kinds {
		word := words[i]
		if rec := env.do(t, "POST", k.base+"/new", k.form); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: create: %d %s", k.table, rec.Code, rec.Body.String())
		}
		sabotageDB(t, env, "UPDATE "+k.table+" SET name = '"+word+"' WHERE name = '"+k.form.Get("name")+"'")

		// Saved again under the name it already has.
		k.form.Set("name", word)
		k.form.Set("description", "edited")
		if rec := env.do(t, "POST", k.base+"/"+word+"/edit", k.form); rec.Code != http.StatusSeeOther {
			t.Errorf("%s: editing an existing %q: code=%d, want it saved", k.table, word, rec.Code)
		}
		// Renamed to something else.
		k.form.Set("name", "renamed_"+word)
		if rec := env.do(t, "POST", k.base+"/"+word+"/edit", k.form); rec.Code != http.StatusSeeOther {
			t.Errorf("%s: renaming %q away: code=%d, want it saved", k.table, word, rec.Code)
		}
		// And not back again.
		k.form.Set("name", word)
		if rec := env.do(t, "POST", k.base+"/renamed_"+word+"/edit", k.form); rec.Code != http.StatusOK {
			t.Errorf("%s: renaming back to %q: code=%d, want the form back with an error", k.table, word, rec.Code)
		}
	}

	// Deleting one that still has the name.
	if _, err := env.store.CreateRPKIServer(store.RPKIServer{Name: "all", Host: "192.0.2.53", Port: 323}); err != nil {
		t.Fatal(err)
	}
	if rec := env.do(t, "POST", "/rpki/all/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", rec.Code)
	}
	if _, err := env.store.GetRPKIServerByName("all"); err != store.ErrNotFound {
		t.Errorf("the RTR server named all should be gone (err %v)", err)
	}
}
