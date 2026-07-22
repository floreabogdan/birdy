package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

func TestChangeStatusTracksAppliedModel(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	status := func() map[string]bool {
		rec := env.do(t, http.MethodGet, "/api/changes/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if !status()["changed"] {
		t.Fatal("unadopted model must report changes")
	}

	in, reason, err := env.srv.renderInput(false)
	if err != nil || reason != "" {
		t.Fatalf("render input: %s %v", reason, err)
	}
	cfg, err := birdconf.Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAppliedConfigHash(hashBytes([]byte(cfg))); err != nil {
		t.Fatal(err)
	}
	if status()["changed"] {
		t.Fatal("matching applied hash reported changes")
	}
}
