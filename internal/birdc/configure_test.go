package birdc

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedServer serves the BIRD banner and then answers every command with
// reply, verbatim. The replies used below are raw captures from BIRD 2.17.5.
func scriptedServer(t *testing.T, reply string) string {
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
					if _, err := r.ReadString('\n'); err != nil {
						return
					}
					if _, err := conn.Write([]byte(reply)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return path
}

// Only "0018 Reconfiguration confirmed" means BIRD kept an armed reconfigure.
// With no timer armed — the timeout already fired, BIRD restarted, or a plain
// `birdc configure` disarmed it — BIRD answers "0019 Nothing to do", which is
// not an 8xxx error but must not read as a successful confirm either: BIRD is
// running some other config, not the one birdy is about to record as kept.
func TestConfigureConfirmOnlyAcceptsConfirmed(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		wantOK   bool
		wantCode int
	}{
		{"armed", "0018 Reconfiguration confirmed\n", true, CodeReconfigConfirmed},
		{"nothing armed", "0019 Nothing to do\n", false, CodeNothingToDo},
		{"refused", "8007 Access denied\n", false, 8007},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{path: scriptedServer(t, tc.reply)}
			res, err := c.ConfigureConfirm(context.Background())
			if err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if res.OK != tc.wantOK || res.Code != tc.wantCode {
				t.Fatalf("confirm on %q = OK %v code %d, want OK %v code %d",
					tc.reply, res.OK, res.Code, tc.wantOK, tc.wantCode)
			}
		})
	}
}

// BIRD reports a protocol it cannot reload — a BGP peer without route refresh,
// or any direct protocol — on a line of its own inside the reply, and still ends
// the reply with an ordinary 0000. Consecutive lines with the same code arrive as
// continuation lines of one block. Raw capture from BIRD 2.17.5, with a second
// failure appended to show the continuation form.
func TestReloadReportsFailedProtocols(t *testing.T) {
	reply := "0015-device1: reloading\n" +
		"8006-direct1: reload failed\n" +
		"0015-st1: reloading\n" +
		" b_rr: reloading\n" +
		"8006-b_norr: reload failed\n" +
		" edge2: reload failed\n" +
		"0000 \n"
	c := &Client{path: scriptedServer(t, reply)}
	res, err := c.Reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !res.OK {
		t.Errorf("BIRD accepted the reload; OK should be true, message %q", res.Message)
	}
	want := []string{"direct1", "b_norr", "edge2"}
	if strings.Join(res.Failed, ",") != strings.Join(want, ",") {
		t.Fatalf("Failed = %v, want %v", res.Failed, want)
	}
}
