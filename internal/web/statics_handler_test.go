package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestStaticRouteCRUDFlow(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	// Create a "via" route.
	form := url.Values{
		"prefix": {"198.51.100.0/26"}, "action": {"via"}, "nextHop": {"10.0.0.2"},
		"description": {"behind the switch"}, "enabled": {"on"},
	}
	if rec := env.do(t, "POST", "/library/static-routes/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create failed: %d %s", rec.Code, rec.Body.String())
	}

	routes, err := env.store.ListStaticRoutes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("list: %v %+v", err, routes)
	}
	id := routes[0].ID

	// It shows up in the candidate config.
	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "route 198.51.100.0/26 via 10.0.0.2;") {
		t.Error("the static route should reach the candidate config")
	}

	// It shows up on the list page.
	body = env.do(t, "GET", "/library/static-routes", nil).Body.String()
	if !strings.Contains(body, "198.51.100.0/26") {
		t.Error("the static route should be listed")
	}

	// Delete it.
	if rec := env.do(t, "POST", "/library/static-routes/"+itoa(id)+"/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete failed: %d", rec.Code)
	}
	if routes, _ := env.store.ListStaticRoutes(); len(routes) != 0 {
		t.Error("route was not deleted")
	}
}

func TestStaticRouteRejectsBadInput(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	// via with no next hop: the form comes back with the error, nothing saved.
	form := url.Values{"prefix": {"198.51.100.0/26"}, "action": {"via"}, "enabled": {"on"}}
	rec := env.do(t, "POST", "/library/static-routes/new", form)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "should receive this traffic") {
		t.Fatalf("want the next-hop error, got %d", rec.Code)
	}
	if routes, _ := env.store.ListStaticRoutes(); len(routes) != 0 {
		t.Error("an invalid route should not be saved")
	}
}

// The prefix carries a slash, so static routes are addressed by id, not name.
// A bad id is a 404, not a 500.
func TestStaticRouteBadIDIs404(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, "GET", "/library/static-routes/not-a-number/edit", nil); rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}
