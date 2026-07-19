package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestConfirmPageRendersScopedAction(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/confirm?do=peer-delete&target=transit_v4", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/peers/transit_v4/delete"`) {
		t.Fatalf("confirm form action missing: %s", body)
	}
	if !strings.Contains(body, "Delete peer transit_v4") {
		t.Fatalf("confirm message missing: %s", body)
	}
}

func TestConfirmPageParameterlessAction(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/confirm?do=adopt", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/apply/adopt"`) {
		t.Fatalf("adopt confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfirmPageRejectsUnknownAndBadTarget(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, http.MethodGet, "/confirm?do=nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: status %d", rec.Code)
	}
	// A target that would break out of the intended POST path is rejected.
	if rec := env.do(t, http.MethodGet, "/confirm?do=peer-delete&target=a/b", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("path-injecting target: status %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/confirm?do=peer-delete", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing target: status %d", rec.Code)
	}
}
