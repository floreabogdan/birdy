// Package web is birdy's embedded UI and JSON API: login, the live session
// dashboard, per-peer detail, the on-demand looking glass, the event
// timeline, and settings/snapshot management. No frontend build step —
// server-rendered html/template pages plus a little vanilla JS for polling.
package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/poller"
	"github.com/floreabogdan/birdy/internal/snapshot"
	"github.com/floreabogdan/birdy/internal/store"
	"github.com/floreabogdan/birdy/internal/updatecheck"
)

// birdClient is the subset of *birdc.Client the web layer calls directly
// (session detail and looking-glass queries; the dashboard instead reads
// from the poller's cached snapshot). Kept as an interface so handlers can
// be tested against a fake instead of a live BIRD socket.
//
// Route listings always go through the paginated Page methods, even from
// the looking glass — a peer carrying a full table could otherwise mean
// loading millions of routes into memory to answer one query.
type birdClient interface {
	ProtocolDetail(ctx context.Context, name string) (birdc.ProtocolDetail, error)
	RoutesForPage(ctx context.Context, prefixOrIP string, all bool, offset, limit int) (birdc.RoutePage, error)
	RoutesByProtocolPage(ctx context.Context, name string, all bool, offset, limit int) (birdc.RoutePage, error)
	RoutesExportPage(ctx context.Context, name string, all bool, offset, limit int) (birdc.RoutePage, error)
	RoutesNoExportPage(ctx context.Context, name string, all bool, offset, limit int) (birdc.RoutePage, error)
	RoutesRPKIInvalidPage(ctx context.Context, localASN int64, offset, limit int) (birdc.RoutePage, error)
	// RoutesRPKIInvalidCount is how many there are in total — BIRD counts them, so
	// the dry run can answer "how many would I drop" without listing them all.
	RoutesRPKIInvalidCount(ctx context.Context, localASN int64) ([]birdc.RouteCountEntry, error)

	// The apply pipeline. These act on BIRD's own configured config file, which
	// birdy writes before calling them. ConfigureCheck never changes the running
	// config; ConfigureTimeout applies with an armed auto-revert.
	ConfigureCheck(ctx context.Context) (birdc.ConfigureResult, error)
	ConfigureTimeout(ctx context.Context, seconds int, soft bool) (birdc.ConfigureResult, error)
	ConfigureConfirm(ctx context.Context) (birdc.ConfigureResult, error)
	ConfigureUndo(ctx context.Context) (birdc.ConfigureResult, error)
	// Reload re-runs filters on existing routes without restarting protocols;
	// birdy pairs it with a soft reconfigure so a filter change actually applies.
	Reload(ctx context.Context) (birdc.ConfigureResult, error)
}

// Server is birdy's HTTP handler: it holds the store, the BIRD client, the
// poller's snapshot and the apply configuration, and serves every route.
type Server struct {
	store    *store.Store
	client   birdClient
	poller   *poller.Poller
	snap     *snapshot.Manager
	log      *slog.Logger
	readOnly bool

	// Where the running config lives, where its backups go, how to invoke
	// `bird -p`, and how long an armed reconfigure has before it auto-reverts.
	birdConfPath  string
	birdBackupDir string
	birdBinary    string
	applyTimeout  int

	// notifier delivers apply/rollback events to alert destinations. nil in
	// tests. metrics gates the unauthenticated Prometheus endpoint; peeringDB
	// gates the PeeringDB lookup that dials out to a third party.
	notifier  alertNotifier
	metrics   bool
	peeringDB bool
	bgpq4Bin  string // non-empty enables IRR expansion via bgpq4
	netdiag   bool   // enables ping/traceroute reachability diagnostics
	updates   updateChecker
	// listenAddr is where birdy is bound, for the wide-open warning (see access.go).
	listenAddr string
	tls        bool

	// applyMu serialises everything that touches bird.conf and the pending-apply
	// record. HTTP handlers run concurrently, and two applies at once could both
	// pass the no-pending check, both back up and overwrite the file, and leave
	// two pending versions. One writer at a time.
	applyMu sync.Mutex

	// refreshMu serialises remote-instance health refreshes so the background
	// loop and an operator-triggered refresh cannot overlap and both fire a
	// duplicate up/down alert for a single state transition.
	refreshMu sync.Mutex

	// login throttles failed logins per client IP.
	login *loginLimiter

	// accessMu guards accessList, the parsed access whitelist cached from
	// settings so the per-request gate never hits the database.
	accessMu   sync.RWMutex
	accessList []netip.Prefix

	// histMu guards a short-lived cache of the dashboard's per-session route
	// history. Recomputing it rescans the whole sample window; the samples only
	// change once per poll interval, so a brief cache spares that work on every
	// 5-second dashboard poll regardless of how many clients are open.
	histMu       sync.Mutex
	histCache    map[string]Series
	histComputed time.Time

	mux *http.ServeMux
}

// StartBackground starts work that is independent of browser request latency.
// The command owns the context, so the worker stops cleanly with the service.
func (s *Server) StartBackground(ctx context.Context) {
	go func() {
		interval := 30 * time.Second
		s.refreshInstanceHealth(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshInstanceHealth(ctx)
			}
		}
	}()
}

// alertNotifier is the slice of the dispatcher the web layer needs: fire an
// event out to the configured destinations, and mail a copy of an applied config
// off the box.
type alertNotifier interface {
	Notify(kind, protocol, message string)
	MailConfig(maskedConfig string)
}

// Config is the dependency set and options New needs to build a Server.
type Config struct {
	Store         *store.Store
	Client        birdClient
	Poller        *poller.Poller
	Snapshot      *snapshot.Manager
	Log           *slog.Logger
	ReadOnly      bool
	BirdConfPath  string
	BirdBackupDir string
	BirdBinary    string
	ApplyTimeout  int
	Notifier      alertNotifier
	Metrics       bool
	PeeringDB     bool
	Bgpq4Bin      string
	NetDiag       bool
	UpdateChecker updateChecker
	// ListenAddr is the address birdy is actually bound to. The UI needs it to
	// tell "reachable from anywhere with an allow-all access list" (the fresh
	// install default, worth warning about) from "loopback only" (nothing off-box
	// can reach it regardless).
	ListenAddr string
	// TLS reports whether the outer HTTP server is serving native HTTPS.
	TLS bool
}

// New builds a Server from cfg, applying defaults for any unset paths and
// timeouts, and wiring up the routes.
func New(cfg Config) *Server {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	birdConfPath := cfg.BirdConfPath
	if birdConfPath == "" {
		birdConfPath = "/etc/bird/bird.conf"
	}
	birdBackupDir := cfg.BirdBackupDir
	if birdBackupDir == "" {
		birdBackupDir = "/var/lib/birdy/bird-backups"
	}
	birdBinary := cfg.BirdBinary
	if birdBinary == "" {
		birdBinary = "bird"
	}
	applyTimeout := cfg.ApplyTimeout
	if applyTimeout <= 0 {
		applyTimeout = 60
	}
	updates := cfg.UpdateChecker
	if updates == nil {
		updates = updatecheck.New()
	}
	s := &Server{
		store:         cfg.Store,
		client:        cfg.Client,
		poller:        cfg.Poller,
		snap:          cfg.Snapshot,
		log:           log,
		readOnly:      cfg.ReadOnly,
		birdConfPath:  birdConfPath,
		birdBackupDir: birdBackupDir,
		birdBinary:    birdBinary,
		applyTimeout:  applyTimeout,
		notifier:      cfg.Notifier,
		metrics:       cfg.Metrics,
		peeringDB:     cfg.PeeringDB,
		bgpq4Bin:      cfg.Bgpq4Bin,
		netdiag:       cfg.NetDiag,
		updates:       updates,
		listenAddr:    cfg.ListenAddr,
		tls:           cfg.TLS,
		login:         newLoginLimiter(),
		mux:           http.NewServeMux(),
	}
	s.routes()
	s.reloadAccess()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.accessAllowed(r) {
		// A blocked client gets no HTTP response at all: the connection is closed,
		// so a scanner cannot even tell there is a service listening. Falls back to
		// a bare 403 when the connection cannot be hijacked (e.g. under test).
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusForbidden)
		return
	}
	secure := s.cookieSecure(r)
	setSecurityHeaders(w, secure)
	if !sameOriginWrite(r, secure) {
		http.Error(w, "cross-origin write rejected", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// sameOriginWrite rejects browser write requests originating on another site.
// SameSite=Strict cookies and CSP form-action already cover modern browsers;
// this validates the request at the server as a separate boundary. Requests
// without browser origin headers remain supported for local CLI automation.
func sameOriginWrite(r *http.Request, secure bool) bool {
	if r.Method != http.MethodPost {
		return true
	}
	if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" || site == "same-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	expectedScheme := "http"
	if secure {
		expectedScheme = "https"
	}
	return strings.EqualFold(u.Scheme, expectedScheme) && strings.EqualFold(u.Host, r.Host)
}

// reloadAccess refreshes the cached access whitelist from settings. Called at
// startup and whenever the whitelist is edited.
func (s *Server) reloadAccess() {
	var list []netip.Prefix
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		list, _ = store.ParseAccessWhitelist(settings.AccessWhitelist)
	}
	s.accessMu.Lock()
	s.accessList = list
	s.accessMu.Unlock()
}

func (s *Server) accessAllowed(r *http.Request) bool {
	ip := clientAddr(r)
	if !ip.IsValid() {
		return false
	}
	s.accessMu.RLock()
	defer s.accessMu.RUnlock()
	return store.AccessAllowed(s.accessList, ip)
}

// clientAddr is the request's real TCP peer address — never a spoofable
// X-Forwarded-For header, since birdy is reached directly or over an SSH tunnel,
// not behind a proxy. Callers deny an invalid address rather than letting a
// malformed peer bypass the access allow-list.
func clientAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// setSecurityHeaders hardens every response. birdy serves only its own embedded
// assets and is never framed, so the policy can be tight: no external resource
// loads, no framing, forms post only to birdy itself, and scripts must be loaded
// from embedded static assets. Styles retain inline support because route gauges
// and several compact layout values are data-driven style attributes.
func setSecurityHeaders(w http.ResponseWriter, secure bool) {
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	h.Set("Content-Security-Policy",
		"default-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; "+
			"frame-ancestors 'none'; img-src 'self' data:; "+
			"style-src 'self' 'unsafe-inline'; script-src 'self'")
	// HSTS only over a secure transport (native TLS, or a trusted loopback proxy
	// that terminated TLS) — never on plaintext, where it would pin a scheme birdy
	// is not serving. Pins the browser to HTTPS after the first secure visit,
	// closing the SSL-strip window on later ones.
	if secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func (s *Server) routes() {
	// Public
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	// Prometheus scrape target, unauthenticated and only when explicitly enabled.
	if s.metrics {
		s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	}

	// Authenticated pages
	s.mux.Handle("GET /{$}", s.requireAuth(s.handleDashboard))

	// Peers (birdy's model) and their live sessions. A peer is what birdy says
	// should run; /peers/{name} shows what BIRD is actually running for it. The
	// literal routes below beat the {name} wildcard, whatever the order here.
	s.mux.Handle("GET /peers", s.requireAuth(s.handlePeersList))
	s.mux.Handle("GET /peers/new", s.requireAuth(s.handlePeerNew))
	s.mux.Handle("POST /peers/new", s.requireAuth(s.handlePeerSave))
	// Seed the model from the sessions BIRD is already running — the adoption
	// warm start. Model-only, so it is allowed in read-only mode.
	s.mux.Handle("GET /peers/seed", s.requireAuth(s.handleSeedPage))
	s.mux.Handle("POST /peers/seed", s.requireAuth(s.handleSeedSave))
	if s.peeringDB {
		s.mux.Handle("GET /api/peeringdb/{asn}", s.requireAuth(s.handlePeeringDBLookup))
	}
	if s.bgpq4Bin != "" {
		s.mux.Handle("GET /api/irr/prefixes", s.requireAuth(s.handleIRRPrefixes))
		s.mux.Handle("GET /api/irr/asns", s.requireAuth(s.handleIRRASNs))
	}
	s.mux.Handle("GET /peers/{name}", s.requireAuth(s.handlePeerDetail))
	s.mux.Handle("GET /peers/{name}/edit", s.requireAuth(s.handlePeerEdit))
	s.mux.Handle("POST /peers/{name}/edit", s.requireAuth(s.handlePeerSave))
	s.mux.Handle("POST /peers/{name}/delete", s.requireAuth(s.handlePeerDelete))
	s.mux.Handle("POST /peers/{name}/toggle", s.requireAuth(s.handlePeerToggle))
	// The live view used to live under /sessions; keep old links alive.
	s.mux.Handle("GET /sessions/{name}", s.requireAuth(s.handleLegacySessionDetail))

	// Policies
	s.mux.Handle("GET /policies", s.requireAuth(s.handlePoliciesList))
	s.mux.Handle("GET /policies/new", s.requireAuth(s.handlePolicyNew))
	s.mux.Handle("POST /policies/new", s.requireAuth(s.handlePolicySave))
	s.mux.Handle("GET /policies/{name}/edit", s.requireAuth(s.handlePolicyEdit))
	s.mux.Handle("POST /policies/{name}/edit", s.requireAuth(s.handlePolicySave))
	s.mux.Handle("POST /policies/{name}/delete", s.requireAuth(s.handlePolicyDelete))

	// Library
	s.mux.Handle("GET /library/prefix-sets", s.requireAuth(s.handlePrefixSetsList))
	s.mux.Handle("GET /library/prefix-sets/new", s.requireAuth(s.handlePrefixSetNew))
	s.mux.Handle("POST /library/prefix-sets/new", s.requireAuth(s.handlePrefixSetSave))
	s.mux.Handle("GET /library/prefix-sets/{name}/edit", s.requireAuth(s.handlePrefixSetEdit))
	s.mux.Handle("POST /library/prefix-sets/{name}/edit", s.requireAuth(s.handlePrefixSetSave))
	s.mux.Handle("POST /library/prefix-sets/{name}/delete", s.requireAuth(s.handlePrefixSetDelete))
	s.mux.Handle("POST /library/prefix-sets/{name}/toggle", s.requireAuth(s.handlePrefixSetToggle))
	s.mux.Handle("GET /library/as-sets", s.requireAuth(s.handleASSetsList))
	s.mux.Handle("GET /library/as-sets/new", s.requireAuth(s.handleASSetNew))
	s.mux.Handle("POST /library/as-sets/new", s.requireAuth(s.handleASSetSave))
	s.mux.Handle("GET /library/as-sets/{name}/edit", s.requireAuth(s.handleASSetEdit))
	s.mux.Handle("POST /library/as-sets/{name}/edit", s.requireAuth(s.handleASSetSave))
	s.mux.Handle("POST /library/as-sets/{name}/delete", s.requireAuth(s.handleASSetDelete))
	if s.bgpq4Bin != "" {
		s.mux.Handle("POST /library/as-sets/{name}/refresh", s.requireAuth(s.handleASSetRefresh))
	}
	s.mux.Handle("GET /library/communities", s.requireAuth(s.handleCommunitiesList))
	s.mux.Handle("GET /library/communities/new", s.requireAuth(s.handleCommunityNew))
	s.mux.Handle("POST /library/communities/new", s.requireAuth(s.handleCommunitySave))
	s.mux.Handle("GET /library/communities/{name}/edit", s.requireAuth(s.handleCommunityEdit))
	s.mux.Handle("POST /library/communities/{name}/edit", s.requireAuth(s.handleCommunitySave))
	s.mux.Handle("POST /library/communities/{name}/delete", s.requireAuth(s.handleCommunityDelete))
	s.mux.Handle("GET /library/static-routes", s.requireAuth(s.handleStaticRoutesList))
	s.mux.Handle("GET /library/static-routes/new", s.requireAuth(s.handleStaticRouteNew))
	s.mux.Handle("POST /library/static-routes/new", s.requireAuth(s.handleStaticRouteSave))
	s.mux.Handle("GET /library/static-routes/{id}/edit", s.requireAuth(s.handleStaticRouteEdit))
	s.mux.Handle("POST /library/static-routes/{id}/edit", s.requireAuth(s.handleStaticRouteSave))
	s.mux.Handle("POST /library/static-routes/{id}/delete", s.requireAuth(s.handleStaticRouteDelete))

	// RPKI
	s.mux.Handle("GET /rpki", s.requireAuth(s.handleRPKIPage))
	s.mux.Handle("GET /rpki/new", s.requireAuth(s.handleRPKINew))
	s.mux.Handle("POST /rpki/new", s.requireAuth(s.handleRPKISave))
	s.mux.Handle("GET /rpki/{name}/edit", s.requireAuth(s.handleRPKIEdit))
	s.mux.Handle("POST /rpki/{name}/edit", s.requireAuth(s.handleRPKISave))
	s.mux.Handle("POST /rpki/{name}/delete", s.requireAuth(s.handleRPKIDelete))

	// BMP monitoring stations
	s.mux.Handle("GET /bmp", s.requireAuth(s.handleBMPPage))
	s.mux.Handle("GET /bmp/new", s.requireAuth(s.handleBMPNew))
	s.mux.Handle("POST /bmp/new", s.requireAuth(s.handleBMPSave))
	s.mux.Handle("GET /bmp/{name}/edit", s.requireAuth(s.handleBMPEdit))
	s.mux.Handle("POST /bmp/{name}/edit", s.requireAuth(s.handleBMPSave))
	s.mux.Handle("POST /bmp/{name}/delete", s.requireAuth(s.handleBMPDelete))

	s.mux.Handle("GET /changes", s.requireAuth(s.handleChanges))
	s.mux.Handle("POST /apply", s.requireAuth(s.handleApply))
	s.mux.Handle("POST /apply/confirm", s.requireAuth(s.handleApplyConfirm))
	s.mux.Handle("POST /apply/rollback", s.requireAuth(s.handleApplyRollback))
	s.mux.Handle("POST /apply/adopt", s.requireAuth(s.handleAdopt))
	s.mux.Handle("GET /changes/history", s.requireAuth(s.handleHistory))
	s.mux.Handle("GET /changes/history/{id}", s.requireAuth(s.handleVersion))
	s.mux.Handle("POST /changes/history/{id}/reapply", s.requireAuth(s.handleReapply))

	s.mux.Handle("GET /timeline", s.requireAuth(s.handleTimeline))
	s.mux.Handle("GET /export/sessions", s.requireAuth(s.handleSessionExport))
	s.mux.Handle("GET /export/events", s.requireAuth(s.handleEventExport))
	s.mux.Handle("GET /export/config", s.requireAuth(s.handleConfigExport))
	s.mux.Handle("GET /lg", s.requireAuth(s.handleLookingGlass))
	s.mux.Handle("GET /diagnostics", s.requireAuth(s.handleDiagnostics))
	s.mux.Handle("GET /settings", s.requireAuth(s.handleSettingsPage))
	s.mux.Handle("GET /instances", s.requireAuth(s.handleInstancesPage))
	s.mux.Handle("GET /instances/{id}", s.requireAuth(s.handleInstanceDetail))
	s.mux.Handle("POST /instances/add", s.requireAuth(s.handleInstanceAdd))
	s.mux.Handle("POST /instances/{id}/metadata", s.requireAuth(s.handleInstanceMetadata))
	s.mux.Handle("POST /instances/{id}/rename", s.requireAuth(s.handleInstanceRename))
	s.mux.Handle("POST /instances/local/rename", s.requireAuth(s.handleLocalInstanceRename))
	s.mux.Handle("POST /instances/{id}/delete", s.requireAuth(s.handleInstanceDelete))
	s.mux.Handle("GET /instances/select", s.requireAuth(s.handleInstanceSelect))
	s.mux.Handle("POST /settings/identity", s.requireAuth(s.handleSettingsIdentity))
	s.mux.Handle("POST /settings/bogons", s.requireAuth(s.handleSettingsBogons))
	s.mux.Handle("POST /settings/raw", s.requireAuth(s.handleSettingsRaw))
	s.mux.Handle("POST /settings/access", s.requireAuth(s.handleSettingsAccess))
	s.mux.Handle("POST /settings/instance-token", s.requireAuth(s.handleSettingsInstanceToken))
	s.mux.Handle("POST /settings/instance-token/revoke", s.requireAuth(s.handleSettingsInstanceTokenRevoke))
	s.mux.Handle("POST /settings/instance-token/{id}/revoke", s.requireAuth(s.handleSettingsInstanceTokenRevokeOne))
	s.mux.Handle("GET /updates", s.requireAuth(s.handleUpdatesPage))
	s.mux.Handle("POST /updates/channel", s.requireAuth(s.handleUpdateChannel))

	// Alerts (destinations for session notifications).
	s.mux.Handle("GET /alerts", s.requireAuth(s.handleAlertsList))
	s.mux.Handle("GET /alerts/new", s.requireAuth(s.handleAlertNew))
	s.mux.Handle("POST /alerts/new", s.requireAuth(s.handleAlertSave))
	s.mux.Handle("GET /alerts/{id}/edit", s.requireAuth(s.handleAlertEdit))
	s.mux.Handle("POST /alerts/{id}/edit", s.requireAuth(s.handleAlertSave))
	s.mux.Handle("POST /alerts/{id}/delete", s.requireAuth(s.handleAlertDelete))
	s.mux.Handle("POST /alerts/{id}/test", s.requireAuth(s.handleAlertTest))

	// The logged-in operator's own account: username and password.
	s.mux.Handle("GET /profile", s.requireAuth(s.handleProfilePage))
	s.mux.Handle("POST /profile/identity", s.requireAuth(s.handleProfileIdentity))
	s.mux.Handle("POST /profile/password", s.requireAuth(s.handleProfilePassword))
	s.mux.Handle("POST /logout", s.requireAuth(s.handleLogout))

	// Live preview: render a form's BIRD code without saving. Read-only-safe.
	s.mux.Handle("POST /peers/preview", s.requireAuth(s.handlePeerPreview))
	s.mux.Handle("POST /policies/preview", s.requireAuth(s.handlePolicyPreview))
	s.mux.Handle("POST /library/prefix-sets/preview", s.requireAuth(s.handlePrefixSetPreview))
	s.mux.Handle("POST /library/as-sets/preview", s.requireAuth(s.handleASSetPreview))
	s.mux.Handle("POST /library/static-routes/preview", s.requireAuth(s.handleStaticRoutePreview))

	// Authenticated JSON API
	s.mux.Handle("GET /api/dashboard", s.requireDashboardAuth(s.apiDashboard))
	s.mux.Handle("GET /confirm", s.requireAuth(s.handleConfirm))
	s.mux.Handle("GET /api/me", s.requireAuth(s.apiMe))
	s.mux.Handle("GET /api/instances", s.requireAuth(s.apiInstances))
	s.mux.Handle("POST /api/instances/refresh", s.requireAuth(s.apiInstancesRefresh))
	s.mux.Handle("POST /api/instances/test", s.requireAuth(s.apiInstanceTest))
	s.mux.Handle("GET /api/instances/activity", s.requireAuth(s.apiInstanceActivity))
	s.mux.Handle("GET /api/peers/{name}", s.requireAuth(s.apiPeerDetail))
	s.mux.Handle("GET /api/peers/{name}/routes", s.requireAuth(s.apiPeerRoutes))
	s.mux.Handle("GET /api/events", s.requireDashboardAuth(s.apiEvents))
	s.mux.Handle("GET /api/lg", s.requireAuth(s.apiLookingGlass))
	s.mux.Handle("GET /api/snapshot/download", s.requireAuth(s.apiSnapshotDownload))
	s.mux.Handle("GET /api/backup/download", s.requireAuth(s.handleBackupDownload))
	s.mux.Handle("POST /api/snapshot/restore", s.requireAuth(s.apiSnapshotRestore))
	s.mux.Handle("GET /api/alerts/summary", s.requireAuth(s.apiAlertsSummary))
}
