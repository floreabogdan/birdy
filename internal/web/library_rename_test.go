package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"
)

// editorAction is where a re-rendered library form posts.
var editorAction = regexp.MustCompile(`<form method="post" action="([^"]*)"[^>]*data-editor`)

// A rename that fails validation re-renders the form with what the operator
// typed, and the form must still post to the name the object is stored under.
// Posting to the typed name turns the corrected resubmit into an edit of
// whatever already has that name: renaming one prefix set onto a sibling's
// name, then fixing the name, overwrote the sibling with the first set's
// entries and left the first set unrenamed.
func TestFailedLibraryRenamePostsBackToTheStoredName(t *testing.T) {
	for _, c := range []struct {
		kind, base string
		form       func(name string, n int) url.Values
		id         func(env *testEnv, name string) (int64, error)
	}{
		{"prefix set", "/library/prefix-sets", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "family": {"ipv4"}, "entries": {fmt.Sprintf("198.51.%d.0/24", n)}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetPrefixSetByName(name)
			return v.ID, err
		}},
		{"AS set", "/library/as-sets", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "entries": {fmt.Sprint(64600 + n)}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetASSetByName(name)
			return v.ID, err
		}},
		{"community", "/library/communities", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "value": {fmt.Sprintf("65000:%d", n)}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetCommunityDefByName(name)
			return v.ID, err
		}},
		{"RPKI server", "/rpki", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "host": {fmt.Sprintf("rtr%d.example.net", n)}, "port": {"8282"},
				"refresh": {"900"}, "expire": {"172800"}, "enabled": {"on"}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetRPKIServerByName(name)
			return v.ID, err
		}},
		{"BMP station", "/bmp", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "address": {fmt.Sprintf("203.0.113.%d", n)}, "port": {"1790"}, "enabled": {"on"}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetBMPStationByName(name)
			return v.ID, err
		}},
		{"policy", "/policies", func(name string, n int) url.Values {
			return url.Values{"name": {name}, "direction": {"import"}, "defaultRoute": {"reject"},
				"bogonAsns": {"off"}, "rov": {"off"}}
		}, func(env *testEnv, name string) (int64, error) {
			v, err := env.store.GetPolicyByName(name)
			return v.ID, err
		}},
	} {
		t.Run(c.kind, func(t *testing.T) {
			env := newTestEnv(t, false)
			for i, name := range []string{"CUST_A", "CUST_C"} {
				if rec := env.do(t, "POST", c.base+"/new", c.form(name, i+1)); rec.Code != http.StatusSeeOther {
					t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body)
				}
			}
			idA, _ := c.id(env, "CUST_A")
			idC, _ := c.id(env, "CUST_C")

			rec := env.do(t, "POST", c.base+"/CUST_A/edit", c.form("CUST_C", 1))
			if rec.Code != http.StatusOK {
				t.Fatalf("a rename onto a sibling's name should re-render the form: %d", rec.Code)
			}
			m := editorAction.FindStringSubmatch(rec.Body.String())
			if m == nil {
				t.Fatal("the re-rendered page has no editor form")
			}
			if want := c.base + "/CUST_A/edit"; m[1] != want {
				t.Errorf("the re-rendered form posts to %s, want %s", m[1], want)
			}

			// The operator corrects the name and resubmits the same form.
			if rec := env.do(t, "POST", m[1], c.form("CUST_D", 1)); rec.Code != http.StatusSeeOther {
				t.Fatalf("corrected resubmit: %d %s", rec.Code, rec.Body)
			}
			if id, err := c.id(env, "CUST_D"); err != nil || id != idA {
				t.Errorf("CUST_D should be CUST_A renamed (id %d); got id %d, err %v", idA, id, err)
			}
			if id, err := c.id(env, "CUST_C"); err != nil || id != idC {
				t.Errorf("CUST_C should be untouched (id %d); got id %d, err %v", idC, id, err)
			}
		})
	}
}
