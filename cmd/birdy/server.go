package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/notify"
	"github.com/floreabogdan/birdy/internal/poller"
	"github.com/floreabogdan/birdy/internal/snapshot"
	"github.com/floreabogdan/birdy/internal/store"
	"github.com/floreabogdan/birdy/internal/web"
)

func cmdServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	dbPath := fs.String("db", defaultDBPath, "path to birdy's SQLite database")
	socketPath := fs.String("socket", "", "override BIRD control socket path (defaults to the value set by \"birdy init\")")
	listen := fs.String("listen", "", "override listen address (defaults to the value set by \"birdy init\")")
	readOnly := fs.Bool("read-only", false, "run as a pure viewer: never write bird.conf, never issue write commands to BIRD")
	birdConf := fs.String("bird-conf", defaultConfigDir+"/bird.conf", "path to the running BIRD config birdy reads and (unless --read-only) writes")
	birdBackupDir := fs.String("bird-backup-dir", defaultBirdBackupDir, "where a copy of bird.conf is kept before each apply overwrites it")
	birdBinary := fs.String("bird-binary", defaultBirdBinary, `bird executable used for "bird -p" config checks`)
	applyTimeout := fs.Int("apply-timeout", 60, "seconds an applied config has to be confirmed before BIRD auto-reverts it")
	pollInterval := fs.Duration("poll-interval", 4*time.Second, "how often to poll BIRD for session state")
	snapshotDir := fs.String("snapshot-dir", defaultSnapshotDir, "directory for nightly database snapshots")
	snapshotInterval := fs.Duration("snapshot-interval", 24*time.Hour, "how often to take a nightly database snapshot")
	snapshotRetain := fs.Int("snapshot-retain", defaultSnapshotKeep, "number of nightly snapshots to keep")
	connectTimeout := fs.Duration("connect-timeout", 30*time.Second, "how long to retry connecting to BIRD at startup")
	alertCooldown := fs.Duration("alert-cooldown", 5*time.Minute, "suppress a repeat alert for the same session within this window (0 disables)")
	dropRatio := fs.Float64("prefix-drop-ratio", 0.5, "alert when a session's imported routes fall to this fraction of the previous poll (0 disables)")
	metrics := fs.Bool("metrics", true, "serve the Prometheus /metrics endpoint (it is unauthenticated, so it stays closed until the access list is narrowed); --metrics=false disables it")
	peeringDB := fs.Bool("peeringdb", true, "PeeringDB lookups on the peer form (dials out to peeringdb.com); --peeringdb=false disables it")
	bgpq4 := fs.String("bgpq4", "auto", `IRR AS-SET expansion via bgpq4: "auto" uses it when installed, "off" disables it, or give a path`)
	netdiag := fs.Bool("netdiag", true, "ping/traceroute diagnostics from the router, when those tools are installed; --netdiag=false disables it")
	driftInterval := fs.Duration("drift-check-interval", 30*time.Second, "how often to check whether bird.conf changed outside birdy, alerting if it did (0 disables)")
	sampleInterval := fs.Duration("sample-interval", time.Minute, "how often to record a per-session route-count point for the dashboard history sparklines (0 disables)")
	sampleRetain := fs.Duration("sample-retain", 7*24*time.Hour, "how long to keep route-count history samples")
	irrRefreshInterval := fs.Duration("irr-refresh-interval", 24*time.Hour, "how often to re-expand auto-refresh prefix sets and AS sets from IRR via bgpq4 (0 disables; requires --bgpq4)")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := snapshot.ApplyPendingRestore(*dbPath, log); err != nil {
		return fmt.Errorf("apply pending restore: %w", err)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	// Refuse to run half-broken. birdy keeps its OWN state in this file — logins,
	// events, route history — even with --read-only, which only stops it writing
	// bird.conf. Without this check an unwritable database still starts, still
	// serves a login page, and fails on the first login with "internal error",
	// which tells the operator nothing.
	if err := st.CheckWritable(); err != nil {
		return fmt.Errorf(`the database at %s is not writable by the user birdy runs as: %w

birdy stores its own state there (logins, events, history) even in --read-only mode.
This usually means "birdy init" ran as root while the service runs as the birdy user.
Fix it with:

  sudo chown -R birdy:birdy %s`, *dbPath, err, filepath.Dir(*dbPath))
	}

	settings, ok, err := st.GetSettings()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("birdy has not been initialized — run \"birdy init\" first")
	}

	effSocket := firstNonEmpty(*socketPath, settings.BirdSocketPath, defaultSocketPath)
	effListen := firstNonEmpty(*listen, settings.ListenAddr, defaultListen)
	feat := detectFeatures(*bgpq4, *netdiag, *peeringDB, *metrics, log)

	log.Info("connecting to BIRD", "socket", effSocket)
	client, err := dialWithRetry(effSocket, *connectTimeout, log)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dispatcher := notify.NewDispatcher(st, log, *alertCooldown)
	p := poller.New(client, st, *pollInterval, log)
	p.SetNotifier(dispatcher)
	p.SetDropRatio(*dropRatio)
	p.SetSampling(*sampleInterval, *sampleRetain)
	go p.Run(ctx)

	snapMgr := snapshot.NewManager(*dbPath, *snapshotDir, *snapshotRetain)
	go snapMgr.RunNightly(ctx, st, *snapshotInterval, log)

	srv := web.New(web.Config{
		Store: st, Client: client, Poller: p, Snapshot: snapMgr, Log: log, ReadOnly: *readOnly,
		BirdConfPath: *birdConf, BirdBackupDir: *birdBackupDir, BirdBinary: *birdBinary,
		ApplyTimeout: *applyTimeout, Notifier: dispatcher, Metrics: feat.Metrics, PeeringDB: feat.PeeringDB,
		Bgpq4Bin: feat.Bgpq4Bin, NetDiag: feat.NetDiag, ListenAddr: effListen,
	})

	// Alert if the config on disk changes out from under birdy (inert until birdy
	// owns a config, so a read-only viewer never false-alarms).
	go srv.WatchDrift(ctx, *driftInterval)

	// Keep IRR-expanded prefix sets current (model only — never auto-applied).
	go srv.RunIRRRefresh(ctx, *irrRefreshInterval)

	httpServer := &http.Server{Addr: effListen, Handler: srv}
	errCh := make(chan error, 1)
	go func() {
		log.Info("birdy listening", "addr", effListen, "readOnly", *readOnly)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func dialWithRetry(socketPath string, maxWait time.Duration, log *slog.Logger) (*birdc.Client, error) {
	deadline := time.Now().Add(maxWait)
	for {
		c, err := birdc.Dial(socketPath, 3*time.Second)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not connect to BIRD at %s after %s: %w", socketPath, maxWait, err)
		}
		log.Warn("BIRD not reachable yet, retrying", "socket", socketPath, "error", err)
		time.Sleep(2 * time.Second)
	}
}
