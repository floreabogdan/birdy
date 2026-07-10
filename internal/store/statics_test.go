package store

import "testing"

func TestStaticRouteValidate(t *testing.T) {
	cases := []struct {
		name  string
		r     StaticRoute
		field string // the error key expected, "" for valid
	}{
		{"valid via", StaticRoute{Prefix: "192.0.2.0/24", Action: "via", NextHop: "10.0.0.2"}, ""},
		{"valid blackhole", StaticRoute{Prefix: "192.0.2.0/24", Action: "blackhole"}, ""},
		{"host bits set", StaticRoute{Prefix: "192.0.2.1/24", Action: "blackhole"}, "prefix"},
		{"bad action", StaticRoute{Prefix: "192.0.2.0/24", Action: "reject"}, "action"},
		{"via without next hop", StaticRoute{Prefix: "192.0.2.0/24", Action: "via"}, "nextHop"},
		{"family mismatch", StaticRoute{Prefix: "192.0.2.0/24", Action: "via", NextHop: "2001:db8::1"}, "nextHop"},
		{"next hop inside prefix", StaticRoute{Prefix: "10.0.0.0/24", Action: "via", NextHop: "10.0.0.2"}, "nextHop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			errs := r.Validate()
			if c.field == "" {
				if len(errs) != 0 {
					t.Fatalf("want valid, got %v", errs)
				}
				return
			}
			if _, ok := errs[c.field]; !ok {
				t.Fatalf("want error on %q, got %v", c.field, errs)
			}
		})
	}
}

// A discard action must not keep a next hop the user typed then changed away from.
func TestStaticRouteClearsNextHopForDiscard(t *testing.T) {
	r := StaticRoute{Prefix: "192.0.2.0/24", Action: "blackhole", NextHop: "10.0.0.2"}
	r.Validate()
	if r.NextHop != "" {
		t.Errorf("blackhole route kept a next hop: %q", r.NextHop)
	}
}

func TestStaticRouteCRUD(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateStaticRoute(StaticRoute{Prefix: "192.0.2.0/24", Action: "blackhole", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetStaticRoute(id)
	if err != nil || got.Prefix != "192.0.2.0/24" {
		t.Fatalf("get: %v %+v", err, got)
	}
	got.Action, got.Enabled = "via", false
	got.NextHop = "10.0.0.2"
	if err := s.UpdateStaticRoute(got); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListStaticRoutes()
	if err != nil || len(list) != 1 || list[0].Action != "via" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.DeleteStaticRoute(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetStaticRoute(id); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
