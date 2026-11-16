package birdc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
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

// echoServer answers every command with a one-line reply. Right after writing
// the reply to a command named "racy" it calls onRacy, so the test can cancel
// the command's context at the same moment the reply lands.
func echoServer(t *testing.T, onRacy func()) string {
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
			go func(conn net.Conn) {
				defer conn.Close()
				conn.Write([]byte(fixtureBanner))
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := conn.Write([]byte("0013 Daemon is up and running\n")); err != nil {
						return
					}
					if strings.TrimSpace(line) == "racy" {
						onRacy()
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return path
}

// A context cancelled just as BIRD's reply arrives must not leave the shared
// client holding a connection its cancel hook has already closed: every later
// command — the poller's included — would fail on that dead socket for good,
// because Command only reconnects a connection it knows is gone. The race is
// narrow, so it is run many times; the reviewer's reproduction hit it within a
// hundred iterations.
func TestCommandRecoversAfterCancelRacingReply(t *testing.T) {
	cancels := make(chan context.CancelFunc, 1)
	path := echoServer(t, func() { (<-cancels)() })
	c, err := Dial(path, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	for i := range 2000 {
		ctx, cancel := context.WithCancel(context.Background())
		cancels <- cancel
		_, _ = c.Command(ctx, "racy") // may succeed or report the cancel; either is fine
		cancel()
		select {
		case <-cancels: // the server never consumed it (the write failed); drain
		default:
		}
		if _, err := c.Command(context.Background(), "show status"); err != nil {
			t.Fatalf("iteration %d: the client stayed broken after a cancelled command: %v", i, err)
		}
	}
}
