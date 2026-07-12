package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	readOnly := fs.Bool("read-only", false, "run as a pure viewer; never issue write commands to BIRD")
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
	metrics := fs.Bool("metrics", false, "expose an unauthenticated Prometheus /metrics endpoint (put it behind your own network controls)")
	peeringDB := fs.Bool("peeringdb", false, "enable PeeringDB lookups on the peer form (dials out to peeringdb.com)")
	bgpq4 := fs.String("bgpq4", "", "path to bgpq4 to enable IRR AS-SET expansion on prefix sets (empty disables; \"bgpq4\" uses PATH)")
	driftInterval := fs.Duration("drift-check-interval", 30*time.Second, "how often to check whether bird.conf changed outside birdy, alerting if it did (0 disables)")
	sampleInterval := fs.Duration("sample-interval", time.Minute, "how often to record a per-session route-count point for the dashboard history sparklines (0 disables)")
	sampleRetain := fs.Duration("sample-retain", 7*24*time.Hour, "how long to keep route-count history samples")
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

	settings, ok, err := st.GetSettings()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("birdy has not been initialized — run \"birdy init\" first")
	}

	effSocket := firstNonEmpty(*socketPath, settings.BirdSocketPath, defaultSocketPath)
	effListen := firstNonEmpty(*listen, settings.ListenAddr, defaultListen)

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
		ApplyTimeout: *applyTimeout, Notifier: dispatcher, Metrics: *metrics, PeeringDB: *peeringDB,
		Bgpq4Bin: *bgpq4,
	})

	// Alert if the config on disk changes out from under birdy (inert until birdy
	// owns a config, so a read-only viewer never false-alarms).
	go srv.WatchDrift(ctx, *driftInterval)

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
