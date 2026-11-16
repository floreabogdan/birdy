package birdc

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Captured from BIRD 2.17.5 against an RTR cache that had just synced. The
// "Last update" is the last successful refresh — it resets every refresh
// interval even when the cache has nothing new, which is why birdy watches the
// serial instead.
const fixtureShowProtocolsAllRPKI = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-cf         RPKI       ---        up     12:18:37.239  Established\n" +
	"1006-  Cache server:     127.0.0.1\n" +
	"       Cache port:       18282\n" +
	"       Status:           Established\n" +
	"       Transport:        Unprotected over TCP\n" +
	"       Protocol version: 1\n" +
	"       Session ID:       25012\n" +
	"       Serial number:    1\n" +
	"       Last update:      before 4.059 s\n" +
	"       Refresh timer   : 5.940/10\n" +
	"       Retry timer     : ---\n" +
	"       Expire timer    : 172795.940/172800\n" +
	"       Channel roa4\n" +
	"         State:          UP\n" +
	"         Table:          rpki4\n" +
	"         Preference:     100\n" +
	"         Input filter:   ACCEPT\n" +
	"         Output filter:  REJECT\n" +
	"         Routes:         3 imported, 0 exported, 3 preferred\n" +
	"       Channel roa6\n" +
	"         State:          UP\n" +
	"         Table:          rpki6\n" +
	"         Preference:     100\n" +
	"         Input filter:   ACCEPT\n" +
	"         Output filter:  REJECT\n" +
	"         Routes:         2 imported, 0 exported, 2 preferred\n" +
	"       No aspa channel\n" +
	"0000 \n"

// Captured from BIRD 2.17.5 for an RTR cache that refuses the connection: no
// session, no serial, and the version BIRD will try next.
const fixtureShowProtocolsAllRPKIDown = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-dead       RPKI       ---        start  12:19:23.203  Transport-Error\n" +
	"1006-  Cache server:     127.0.0.1\n" +
	"       Cache port:       18999\n" +
	"       Status:           Transport-Error\n" +
	"       Transport:        Unprotected over TCP\n" +
	"       Protocol version: 2\n" +
	"       Session ID:       ---\n" +
	"       Serial number:    ---\n" +
	"       Last update:      ---\n" +
	"       Refresh timer   : ---\n" +
	"       Retry timer     : 2.999/5\n" +
	"       Expire timer    : ---\n" +
	"       Channel roa4\n" +
	"         State:          DOWN\n" +
	"0000 \n"

func TestParseProtocolDetailRPKI(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllRPKI))
	if err != nil {
		t.Fatal(err)
	}
	if d.RPKI == nil {
		t.Fatal("RPKI = nil, want the cache state")
	}
	want := RPKICache{Status: "Established", Version: 1, SessionID: 25012, Serial: 1, LastUpdate: "before 4.059 s"}
	if *d.RPKI != want {
		t.Errorf("RPKI = %+v, want %+v", *d.RPKI, want)
	}
	if roa4, roa6 := d.RPKI.ROAs(d.Channels); roa4 != 3 || roa6 != 2 {
		t.Errorf("ROAs = %d/%d, want 3/2", roa4, roa6)
	}
}

func TestParseProtocolDetailRPKINotSynced(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllRPKIDown))
	if err != nil {
		t.Fatal(err)
	}
	if d.RPKI == nil {
		t.Fatal("RPKI = nil, want the cache state")
	}
	want := RPKICache{Status: "Transport-Error", Version: 2, SessionID: -1, Serial: -1, LastUpdate: ""}
	if *d.RPKI != want {
		t.Errorf("RPKI = %+v, want %+v", *d.RPKI, want)
	}
	if roa4, roa6 := d.RPKI.ROAs(d.Channels); roa4 != 0 || roa6 != 0 {
		t.Errorf("ROAs = %d/%d, want 0/0 for a feed that never synced", roa4, roa6)
	}
}

func TestParseProtocolDetailBGPHasNoRPKI(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllBGP))
	if err != nil {
		t.Fatal(err)
	}
	if d.RPKI != nil {
		t.Errorf("RPKI = %+v on a BGP protocol, want nil", *d.RPKI)
	}
}

// scriptedBird answers each command line with the canned reply in replies,
// and reports every command it was sent on the returned channel.
func scriptedBird(t *testing.T, replies map[string]string) (string, <-chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bird.ctl")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	got := make(chan string, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				conn.Write([]byte(fixtureBanner))
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					cmd := strings.TrimSpace(sc.Text())
					got <- cmd
					reply, ok := replies[cmd]
					if !ok {
						reply = "9001 syntax error\n"
					}
					conn.Write([]byte(reply))
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return path, got
}

func TestRestartProtocol(t *testing.T) {
	// The wire bytes BIRD 2.17.5 sends for "restart cf".
	path, sent := scriptedBird(t, map[string]string{"restart cf": "0012-cf: restarted\n0000 \n"})
	c, err := Dial(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.RestartProtocol(context.Background(), "cf"); err != nil {
		t.Fatalf("RestartProtocol: %v", err)
	}
	if cmd := <-sent; cmd != "restart cf" {
		t.Errorf("sent %q, want \"restart cf\"", cmd)
	}
}

func TestRestartProtocolUnknown(t *testing.T) {
	path, _ := scriptedBird(t, map[string]string{})
	c, err := Dial(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.RestartProtocol(context.Background(), "nosuch"); err == nil {
		t.Fatal("RestartProtocol on an unknown protocol returned nil, want an error")
	}
}

// A disabled protocol is not restarted: BIRD says so with an informational code,
// not an error code, so "no error code" alone is not success.
func TestRestartProtocolDisabled(t *testing.T) {
	path, _ := scriptedBird(t, map[string]string{"restart cf": "0008-cf: already disabled\n0000 \n"})
	c, err := Dial(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.RestartProtocol(context.Background(), "cf")
	if err == nil || !strings.Contains(err.Error(), "already disabled") {
		t.Fatalf("err = %v, want one carrying BIRD's \"already disabled\"", err)
	}
}

func TestRestartProtocolRejectsInjection(t *testing.T) {
	c := &Client{}
	if err := c.RestartProtocol(context.Background(), "cf\nconfigure"); err == nil {
		t.Fatal("RestartProtocol accepted a name with a newline")
	}
}
