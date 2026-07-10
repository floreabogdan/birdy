package store

import "testing"

// Cloudflare ships disabled: an enabled server means birdy renders a protocol
// that dials a third party, which is the operator's decision, not a default.
func TestCloudflareIsSeededDisabled(t *testing.T) {
	s := openTest(t)
	srv, err := s.GetRPKIServerByName("cloudflare")
	if err != nil {
		t.Fatalf("cloudflare not seeded: %v", err)
	}
	if srv.Enabled {
		t.Error("the seeded RTR server must be disabled")
	}
	if srv.Host != "rtr.rpki.cloudflare.com" || srv.Port != 8282 {
		t.Errorf("wrong endpoint: %s:%d", srv.Host, srv.Port)
	}
	if srv.Refresh == 0 || srv.Expire <= srv.Refresh {
		t.Errorf("timers should be sane: refresh=%d expire=%d", srv.Refresh, srv.Expire)
	}
	if errs := srv.Validate(); len(errs) != 0 {
		t.Errorf("the seeded server should validate: %v", errs)
	}
}

func TestSeededRPKIServerIsIdempotent(t *testing.T) {
	s := openTest(t)
	before, _ := s.ListRPKIServers()
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListRPKIServers()
	if len(before) != len(after) {
		t.Errorf("seeding ran twice: %d -> %d", len(before), len(after))
	}
}

// An operator who enables it and then edits it must not have their changes
// reverted by a later migration.
func TestSeededRPKIServerIsNotReset(t *testing.T) {
	s := openTest(t)
	srv, err := s.GetRPKIServerByName("cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	srv.Enabled = true
	if err := s.UpdateRPKIServer(srv); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetRPKIServerByName("cloudflare")
	if !again.Enabled {
		t.Error("a re-run migration must not disable a server the operator enabled")
	}
}
