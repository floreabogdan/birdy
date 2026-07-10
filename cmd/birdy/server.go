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
	birdConf := fs.String("bird-conf", defaultConfigDir+"/bird.conf", "path to the running BIRD config, for the Changes diff")
	birdBinary := fs.String("bird-binary", defaultBirdBinary, `bird executable used for "bird -p" config checks`)
	pollInterval := fs.Duration("poll-interval", 4*time.Second, "how often to poll BIRD for session state")
	snapshotDir := fs.String("snapshot-dir", defaultSnapshotDir, "directory for nightly database snapshots")
	snapshotInterval := fs.Duration("snapshot-interval", 24*time.Hour, "how often to take a nightly database snapshot")
	snapshotRetain := fs.Int("snapshot-retain", defaultSnapshotKeep, "number of nightly snapshots to keep")
	connectTimeout := fs.Duration("connect-timeout", 30*time.Second, "how long to retry connecting to BIRD at startup")
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

	p := poller.New(client, st, *pollInterval, log)
	go p.Run(ctx)

	snapMgr := snapshot.NewManager(*dbPath, *snapshotDir, *snapshotRetain)
	go snapMgr.RunNightly(ctx, st, *snapshotInterval, log)

	srv := web.New(web.Config{
		Store: st, Client: client, Poller: p, Snapshot: snapMgr, Log: log, ReadOnly: *readOnly,
		BirdConfPath: *birdConf, BirdBinary: *birdBinary,
	})

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
