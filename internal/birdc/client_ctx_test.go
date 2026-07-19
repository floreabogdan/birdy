package birdc

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// hangAfterBanner serves the BIRD welcome banner and then never replies, so a
// command blocks in readFrame until its context intervenes.
func hangAfterBanner(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bird.ctl")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Banner only; then hold the connection open and silent.
			conn.Write([]byte(fixtureBanner))
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return path
}

func TestCommandContextCancel(t *testing.T) {
	c, err := Dial(hangAfterBanner(t), 30*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { cancel() }()

	start := time.Now()
	_, err = c.Command(ctx, "show status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The Client's own timeout is 30s; cancellation must return well before it.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel took %s — did not abort the blocked read", elapsed)
	}
}

func TestCommandContextDeadline(t *testing.T) {
	c, err := Dial(hangAfterBanner(t), 30*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = c.Command(ctx, "show status")
	if err == nil {
		t.Fatal("want a deadline error, got nil")
	}
	// The short ctx deadline must win over the Client's 30s command timeout.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("deadline took %s — ctx deadline was not applied", elapsed)
	}
}
