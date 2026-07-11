package store

import "testing"

func TestBMPStationCRUD(t *testing.T) {
	s := openTest(t)

	st := BMPStation{
		Name: "collector", Description: "route collector", Address: "198.51.100.10",
		Port: 1790, Enabled: true, PrePolicy: true, PostPolicy: true, TxBufferLimit: 64,
	}
	if errs := st.Validate(); len(errs) != 0 {
		t.Fatalf("valid station rejected: %v", errs)
	}
	id, err := s.CreateBMPStation(st)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetBMPStationByName("collector")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != id || got.Address != "198.51.100.10" || got.Port != 1790 ||
		!got.PrePolicy || !got.PostPolicy || got.TxBufferLimit != 64 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	got.PostPolicy = false
	got.Port = 6543
	if err := s.UpdateBMPStation(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := s.GetBMPStationByName("collector")
	if again.PostPolicy || again.Port != 6543 {
		t.Fatalf("update not persisted: %+v", again)
	}

	if err := s.DeleteBMPStation(again.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetBMPStationByName("collector"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestBMPStationValidate(t *testing.T) {
	cases := map[string]struct {
		st    BMPStation
		field string // the field expected to error, "" means valid
	}{
		"ok":          {BMPStation{Name: "c", Address: "2001:db8::1", Port: 1790}, ""},
		"bad name":    {BMPStation{Name: "1bad", Address: "203.0.113.1", Port: 1790}, "name"},
		"hostname":    {BMPStation{Name: "c", Address: "collector.example.com", Port: 1790}, "address"},
		"no address":  {BMPStation{Name: "c", Address: "", Port: 1790}, "address"},
		"bad port":    {BMPStation{Name: "c", Address: "203.0.113.1", Port: 0}, "port"},
		"huge buffer": {BMPStation{Name: "c", Address: "203.0.113.1", Port: 1790, TxBufferLimit: 99999}, "txBufferLimit"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := tc.st
			errs := st.Validate()
			if tc.field == "" {
				if len(errs) != 0 {
					t.Fatalf("expected valid, got %v", errs)
				}
				return
			}
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("expected error on %q, got %v", tc.field, errs)
			}
		})
	}
}

// An address must be canonicalised on the way in, so the rendered config is
// deterministic regardless of how the operator typed it.
func TestBMPStationCanonicalisesAddress(t *testing.T) {
	st := BMPStation{Name: "c", Address: "2001:DB8:0:0:0:0:0:1", Port: 1790}
	st.Validate()
	if st.Address != "2001:db8::1" {
		t.Fatalf("address not canonicalised: %q", st.Address)
	}
}
