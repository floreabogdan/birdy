package store

import "testing"

func TestInstancesRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateInstance("edge London", "https://london.example:8080", "a-token-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetInstance(id)
	if err != nil || !ok || got.Name != "edge London" || got.Token != "a-token-that-is-long-enough" {
		t.Fatalf("instance=%+v ok=%v err=%v", got, ok, err)
	}
	items, err := s.ListInstances()
	if err != nil || len(items) != 1 {
		t.Fatalf("instances=%+v err=%v", items, err)
	}
	if err := s.DeleteInstance(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetInstance(id); ok {
		t.Fatal("deleted instance still exists")
	}
}

func TestRenameInstancePreservesConnectionDetails(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateInstance("old name", "https://router.example:8080", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameInstance(id, "Core London"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetInstance(id)
	if err != nil || !ok || got.Name != "Core London" || got.BaseURL != "https://router.example:8080" || got.Token != "secret" {
		t.Fatalf("renamed instance=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestInstanceAPITokenVerification(t *testing.T) {
	s := openTest(t)
	if err := s.SaveSettings(Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInstanceAPITokenHash(HashInstanceAPIToken("secret-token")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyInstanceAPIToken("secret-token"); err != nil || !ok {
		t.Fatalf("valid token: ok=%v err=%v", ok, err)
	}
	if ok, err := s.VerifyInstanceAPIToken("wrong-token"); err != nil || ok {
		t.Fatalf("wrong token: ok=%v err=%v", ok, err)
	}
}
