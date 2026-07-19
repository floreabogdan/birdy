package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestInstanceAPIRequiresBearerOrSession(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, http.MethodGet, "/api/instances", nil); rec.Code != http.StatusOK {
		t.Fatalf("session API status=%d", rec.Code)
	}
	if err := env.store.SaveSettings(store.Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("remote-secret")); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Header.Set("Authorization", "Bearer remote-secret")
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "edge_v4") {
		t.Fatalf("bearer dashboard: status=%d body=%s", rec.Code, rec.Body.String())
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	bad.Header.Set("Authorization", "Bearer wrong")
	badRec := httptest.NewRecorder()
	env.srv.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer status=%d", badRec.Code)
	}
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if err := env.store.SaveInstanceAPIToken(store.HashInstanceAPIToken("expired-secret"), expired); err != nil {
		t.Fatal(err)
	}
	expiredReq := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	expiredReq.Header.Set("Authorization", "Bearer expired-secret")
	expiredRec := httptest.NewRecorder()
	env.srv.ServeHTTP(expiredRec, expiredReq)
	if expiredRec.Code != http.StatusUnauthorized {
		t.Fatalf("expired bearer status=%d", expiredRec.Code)
	}
	if err := env.store.RevokeInstanceAPIToken(); err != nil {
		t.Fatal(err)
	}
	revokedReq := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	revokedReq.Header.Set("Authorization", "Bearer expired-secret")
	revokedRec := httptest.NewRecorder()
	env.srv.ServeHTTP(revokedRec, revokedReq)
	if revokedRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer status=%d", revokedRec.Code)
	}
}

// A remote observer authenticates with a bearer token, not a session. It must
// not be able to smuggle a birdy_instance cookie to make us relay another
// federated instance's dashboard with our stored token — the token is scoped to
// reading THIS instance only.
func TestBearerDashboardIgnoresInstanceCookie(t *testing.T) {
	relayed := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayed = true
		t.Errorf("bearer request relayed to remote instance: %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("remote-secret")); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.CreateInstance("remote-edge", remote.URL, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Header.Set("Authorization", "Bearer remote-secret")
	req.AddCookie(&http.Cookie{Name: instanceCookieName, Value: fmt.Sprint(id)})
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer dashboard status=%d", rec.Code)
	}
	if relayed {
		t.Fatal("token holder pivoted to a remote instance via the selection cookie")
	}
	// The response must be this instance's own dashboard, not the remote's.
	if strings.Contains(rec.Body.String(), `"instanceRemote":true`) {
		t.Fatalf("bearer dashboard served a remote view: %s", rec.Body.String())
	}
}

func TestValidateInstanceURLRejectsSSRFTargets(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1:8080",
		"https://[::1]:8080",
		"http://169.254.169.254", // cloud metadata (link-local)
		"http://0.0.0.0:8080",
		"https://[fe80::1]:8080",
	}
	for _, raw := range blocked {
		if _, err := validateInstanceURL(raw); err == nil {
			t.Errorf("validateInstanceURL(%q) = nil error, want rejection", raw)
		}
	}
	allowed := []string{
		"https://router.example.net:8080",
		"http://10.0.0.5:8080",     // private mgmt network — legitimate
		"https://[fd00::1]:8080",   // ULA — legitimate
		"http://192.168.1.2:8080",
	}
	for _, raw := range allowed {
		if _, err := validateInstanceURL(raw); err != nil {
			t.Errorf("validateInstanceURL(%q) = %v, want allowed", raw, err)
		}
	}
}

func TestInstanceAddAndSelect(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/instances/add", url.Values{"name": {"remote"}, "baseURL": {"https://remote.example:8080"}, "token": {strings.Repeat("a", 40)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add status=%d", rec.Code)
	}
	items, err := env.store.ListInstances()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/instances/select?id=0", nil)
	req.AddCookie(env.cookie)
	selectRec := httptest.NewRecorder()
	env.srv.ServeHTTP(selectRec, req)
	if selectRec.Code != http.StatusSeeOther || !strings.Contains(selectRec.Header().Get("Set-Cookie"), instanceCookieName) {
		t.Fatalf("select response: status=%d headers=%v", selectRec.Code, selectRec.Header())
	}
}

func TestInstanceRename(t *testing.T) {
	env := newTestEnv(t, false)
	id, err := env.store.CreateInstance("old", "https://remote.example:8080", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, fmt.Sprintf("/instances/%d/rename", id), url.Values{"name": {"Friendly core"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename status=%d", rec.Code)
	}
	instance, ok, err := env.store.GetInstance(id)
	if err != nil || !ok || instance.Name != "Friendly core" {
		t.Fatalf("instance=%+v ok=%v err=%v", instance, ok, err)
	}
}

func TestLocalInstanceRenameUsesRouterLabel(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "old local", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, "/instances/local/rename", url.Values{"name": {"Core router"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename status=%d", rec.Code)
	}
	settings, ok, err := env.store.GetSettings()
	if err != nil || !ok || settings.RouterLabel != "Core router" {
		t.Fatalf("settings=%+v ok=%v err=%v", settings, ok, err)
	}
}

func TestSelectedRemoteDashboardIsReadOnlyView(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 40) {
			t.Fatalf("remote authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instanceName":"remote-edge","status":{"hostname":"remote-edge"},"statusText":"All 1 session up","statusOK":true,"protocols":[{"name":"remote_v6","proto":"BGP","state":"up","info":"Established","up":true}],"sessionUp":1,"totalRoutes":42}`))
	}))
	defer remote.Close()
	env := newTestEnv(t, false)
	id, err := env.store.CreateInstance("remote-edge", remote.URL, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.AddCookie(env.cookie)
	req.AddCookie(&http.Cookie{Name: instanceCookieName, Value: fmt.Sprint(id)})
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "remote_v6") || !strings.Contains(rec.Body.String(), `"instanceRemote":true`) {
		t.Fatalf("remote dashboard: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
