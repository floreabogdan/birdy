// Package web is birdy's embedded UI and JSON API: login, the live session
// dashboard, per-peer detail, the on-demand looking glass, the event
// timeline, and settings/snapshot management. No frontend build step —
// server-rendered html/template pages plus a little vanilla JS for polling.
package web

import (
	"log/slog"
	"net/http"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/poller"
	"github.com/floreabogdan/birdy/internal/snapshot"
	"github.com/floreabogdan/birdy/internal/store"
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
	ProtocolDetail(name string) (birdc.ProtocolDetail, error)
	RoutesForPage(prefixOrIP string, all bool, offset, limit int) (birdc.RoutePage, error)
	RoutesByProtocolPage(name string, offset, limit int) (birdc.RoutePage, error)
	RoutesExportPage(name string, offset, limit int) (birdc.RoutePage, error)
	RoutesNoExportPage(name string, offset, limit int) (birdc.RoutePage, error)

	// The apply pipeline. These act on BIRD's own configured config file, which
	// birdy writes before calling them. ConfigureCheck never changes the running
	// config; ConfigureTimeout applies with an armed auto-revert.
	ConfigureCheck() (birdc.ConfigureResult, error)
	ConfigureTimeout(seconds int, soft bool) (birdc.ConfigureResult, error)
	ConfigureConfirm() (birdc.ConfigureResult, error)
	ConfigureUndo() (birdc.ConfigureResult, error)
}

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

	mux *http.ServeMux
}

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
}

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
		mux:           http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Public
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	// Authenticated pages
	s.mux.Handle("GET /{$}", s.requireAuth(s.handleDashboard))

	// Peers (birdy's model) and their live sessions. A peer is what birdy says
	// should run; /peers/{name} shows what BIRD is actually running for it. The
	// literal routes below beat the {name} wildcard, whatever the order here.
	s.mux.Handle("GET /peers", s.requireAuth(s.handlePeersList))
	s.mux.Handle("GET /peers/new", s.requireAuth(s.handlePeerNew))
	s.mux.Handle("POST /peers/new", s.requireAuth(s.handlePeerSave))
	s.mux.Handle("GET /peers/{name}", s.requireAuth(s.handlePeerDetail))
	s.mux.Handle("GET /peers/{name}/edit", s.requireAuth(s.handlePeerEdit))
	s.mux.Handle("POST /peers/{name}/edit", s.requireAuth(s.handlePeerSave))
	s.mux.Handle("POST /peers/{name}/delete", s.requireAuth(s.handlePeerDelete))
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
	s.mux.Handle("GET /library/as-sets", s.requireAuth(s.handleASSetsList))
	s.mux.Handle("GET /library/as-sets/new", s.requireAuth(s.handleASSetNew))
	s.mux.Handle("POST /library/as-sets/new", s.requireAuth(s.handleASSetSave))
	s.mux.Handle("GET /library/as-sets/{name}/edit", s.requireAuth(s.handleASSetEdit))
	s.mux.Handle("POST /library/as-sets/{name}/edit", s.requireAuth(s.handleASSetSave))
	s.mux.Handle("POST /library/as-sets/{name}/delete", s.requireAuth(s.handleASSetDelete))
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

	s.mux.Handle("GET /changes", s.requireAuth(s.handleChanges))
	s.mux.Handle("POST /apply", s.requireAuth(s.handleApply))
	s.mux.Handle("POST /apply/confirm", s.requireAuth(s.handleApplyConfirm))
	s.mux.Handle("POST /apply/rollback", s.requireAuth(s.handleApplyRollback))
	s.mux.Handle("POST /apply/adopt", s.requireAuth(s.handleAdopt))
	s.mux.Handle("GET /changes/history", s.requireAuth(s.handleHistory))
	s.mux.Handle("GET /changes/history/{id}", s.requireAuth(s.handleVersion))
	s.mux.Handle("POST /changes/history/{id}/reapply", s.requireAuth(s.handleReapply))

	s.mux.Handle("GET /timeline", s.requireAuth(s.handleTimeline))
	s.mux.Handle("GET /lg", s.requireAuth(s.handleLookingGlass))
	s.mux.Handle("GET /settings", s.requireAuth(s.handleSettingsPage))
	s.mux.Handle("POST /settings/identity", s.requireAuth(s.handleSettingsIdentity))
	s.mux.Handle("POST /settings/bogons", s.requireAuth(s.handleSettingsBogons))
	s.mux.Handle("POST /settings/raw", s.requireAuth(s.handleSettingsRaw))

	// Alerts (destinations for session notifications).
	s.mux.Handle("GET /alerts", s.requireAuth(s.handleAlertsList))
	s.mux.Handle("GET /alerts/new", s.requireAuth(s.handleAlertNew))
	s.mux.Handle("POST /alerts/new", s.requireAuth(s.handleAlertSave))
	s.mux.Handle("GET /alerts/{id}/edit", s.requireAuth(s.handleAlertEdit))
	s.mux.Handle("POST /alerts/{id}/edit", s.requireAuth(s.handleAlertSave))
	s.mux.Handle("POST /alerts/{id}/delete", s.requireAuth(s.handleAlertDelete))
	s.mux.Handle("POST /alerts/{id}/test", s.requireAuth(s.handleAlertTest))
	s.mux.Handle("POST /logout", s.requireAuth(s.handleLogout))

	// Live preview: render a form's BIRD code without saving. Read-only-safe.
	s.mux.Handle("POST /peers/preview", s.requireAuth(s.handlePeerPreview))
	s.mux.Handle("POST /policies/preview", s.requireAuth(s.handlePolicyPreview))
	s.mux.Handle("POST /library/prefix-sets/preview", s.requireAuth(s.handlePrefixSetPreview))
	s.mux.Handle("POST /library/as-sets/preview", s.requireAuth(s.handleASSetPreview))
	s.mux.Handle("POST /library/static-routes/preview", s.requireAuth(s.handleStaticRoutePreview))

	// Authenticated JSON API
	s.mux.Handle("GET /api/dashboard", s.requireAuth(s.apiDashboard))
	s.mux.Handle("GET /api/peers/{name}", s.requireAuth(s.apiPeerDetail))
	s.mux.Handle("GET /api/peers/{name}/routes", s.requireAuth(s.apiPeerRoutes))
	s.mux.Handle("GET /api/events", s.requireAuth(s.apiEvents))
	s.mux.Handle("GET /api/lg", s.requireAuth(s.apiLookingGlass))
	s.mux.Handle("GET /api/snapshot/download", s.requireAuth(s.apiSnapshotDownload))
	s.mux.Handle("POST /api/snapshot/restore", s.requireAuth(s.apiSnapshotRestore))
	s.mux.Handle("GET /api/alerts/summary", s.requireAuth(s.apiAlertsSummary))
}
