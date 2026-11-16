package store

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseAllowedHosts(t *testing.T) {
	got, errs := ParseAllowedHosts("Router1.Example.NET.\n# the NOC's name\nbirdy.noc.example.net, edge_1.lab\n\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if want := []string{"router1.example.net", "birdy.noc.example.net", "edge_1.lab"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, bad := range []string{"http://router1.example.net", "router1.example.net:8080", "*.example.net",
		"192.0.2.7", "[2001:db8::1]", "bad name", "router1..example.net", "-router.example.net", strings.Repeat("a", 64) + ".net"} {
		if _, errs := ParseAllowedHosts(bad); len(errs) != 1 {
			t.Errorf("%q should be refused, got errors %v", bad, errs)
		}
	}
}

// v40 adds settings.allowed_hosts. An upgraded router starts with it empty —
// birdy then answers only to IP addresses and localhost until names are added.
func TestMigrateAllowedHostsFromV39(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v39.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE settings DROP COLUMN allowed_hosts; PRAGMA user_version = 39;`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("open/migrate v39 database: %v", err)
	}
	defer st.Close()
	got, ok, err := st.GetSettings()
	if err != nil || !ok {
		t.Fatalf("settings after migrate: ok=%v err=%v", ok, err)
	}
	if got.AllowedHosts != "" || got.RouterID != "192.0.2.1" {
		t.Fatalf("migrated settings: allowed hosts %q, router ID %q", got.AllowedHosts, got.RouterID)
	}
	if err := st.SaveAllowedHosts("router1.example.net"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.GetSettings(); got.AllowedHosts != "router1.example.net" {
		t.Fatalf("allowed hosts did not round-trip: %q", got.AllowedHosts)
	}
}
