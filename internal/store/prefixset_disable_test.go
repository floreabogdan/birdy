package store

import "testing"

func TestPrefixSetDisableToggle(t *testing.T) {
	s := openTest(t)
	id, err := s.CreatePrefixSet(PrefixSet{
		Name: "CUST", Family: FamilyV4,
		Entries: []PrefixEntry{{Prefix: "203.0.113.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A freshly created set is enabled — the column default is "on".
	if ps, _ := s.GetPrefixSet(id); ps.Disabled {
		t.Fatal("a new prefix set must default to enabled")
	}

	if err := s.SetPrefixSetDisabled(id, true); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.GetPrefixSet(id)
	if !ps.Disabled {
		t.Fatal("SetPrefixSetDisabled(true) should disable the set")
	}

	// Editing the set through the form must not silently re-enable it — the update
	// leaves the disabled column alone, the toggle owns it.
	ps.Description = "edited"
	if err := s.UpdatePrefixSet(ps); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.GetPrefixSet(id); !ps.Disabled {
		t.Error("editing a set must preserve its disabled state")
	}

	if err := s.SetPrefixSetDisabled(id, false); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.GetPrefixSet(id); ps.Disabled {
		t.Error("SetPrefixSetDisabled(false) should re-enable the set")
	}
}

// A system set (a bogon list) is wired into generated filters, so it cannot be
// switched off — the store refuses it rather than break the "reject bogons" checks.
func TestSystemPrefixSetCannotBeDisabled(t *testing.T) {
	s := openTest(t)
	v4, err := s.GetBogonSet(FamilyV4)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrefixSetDisabled(v4.ID, true); err == nil {
		t.Error("a system set must not be disablable")
	}
	if ps, _ := s.GetPrefixSet(v4.ID); ps.Disabled {
		t.Error("the system set must remain enabled after a refused toggle")
	}
}
