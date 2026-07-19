package birdc

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBirdServer serves a single canned reply (after the banner) over a unix
// socket, mimicking BIRD's wire protocol. It accepts any number of
// connections (streamRoutes always dials fresh) but only serves the first
// command sent on each.
type fakeBirdServer struct {
	path string
	ln   net.Listener
}

func startFakeBirdServer(t *testing.T, reply string) *fakeBirdServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bird.ctl")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeBirdServer{path: path, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte(fixtureBanner))
				buf := make([]byte, 4096)
				// read (and discard) the command line; a client that stops
				// reading early may never send one before closing, which is fine.
				c.SetReadDeadline(time.Now().Add(2 * time.Second))
				c.Read(buf)
				c.Write([]byte(reply))
				// give the client time to read before we close, then exit —
				// if the client stopped early, this write may partially fail
				// (broken pipe), which is expected and harmless for the test.
				time.Sleep(20 * time.Millisecond)
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

// buildRouteReply synthesizes a valid "show route protocol X" wire reply
// with n distinct unicast routes in one table, matching the real captured
// format (one leading-space continuation line per route after the opening
// "Table ...:" line), each followed by a "via"/"dev" nexthop detail line.
func buildRouteReply(n int) string {
	var b strings.Builder
	b.WriteString("1007-Table master4:\n")
	for i := range n {
		fmt.Fprintf(&b, " 10.0.%d.0/24         unicast [testproto 2026-07-08] * (100)\n", i)
		fmt.Fprintf(&b, " \tvia 192.0.2.%d on eno1\n", i%250)
	}
	b.WriteString("0000 \n")
	return b.String()
}

func TestPaginateFirstPage(t *testing.T) {
	srv := startFakeBirdServer(t, buildRouteReply(500))
	page, err := paginate(context.Background(), srv.path, 5*time.Second, "show route protocol testproto", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tables) != 1 || page.Tables[0].Name != "master4" {
		t.Fatalf("tables = %+v", page.Tables)
	}
	if len(page.Tables[0].Routes) != 10 {
		t.Fatalf("got %d routes, want 10", len(page.Tables[0].Routes))
	}
	if page.Tables[0].Routes[0].Network != "10.0.0.0/24" {
		t.Fatalf("first route = %+v", page.Tables[0].Routes[0])
	}
	if page.Tables[0].Routes[9].Network != "10.0.9.0/24" {
		t.Fatalf("last route on page = %+v", page.Tables[0].Routes[9])
	}
	if page.Tables[0].Routes[0].NextHop != "via 192.0.2.0 on eno1" {
		t.Fatalf("nexthop not attached: %+v", page.Tables[0].Routes[0])
	}
	if !page.HasMore {
		t.Fatal("expected HasMore=true with 500 routes and page size 10")
	}
}

func TestPaginateMiddlePage(t *testing.T) {
	srv := startFakeBirdServer(t, buildRouteReply(500))
	page, err := paginate(context.Background(), srv.path, 5*time.Second, "show route protocol testproto", 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tables[0].Routes) != 5 {
		t.Fatalf("got %d routes, want 5", len(page.Tables[0].Routes))
	}
	if page.Tables[0].Routes[0].Network != "10.0.20.0/24" {
		t.Fatalf("first route on page = %+v, want offset 20", page.Tables[0].Routes[0])
	}
	if !page.HasMore {
		t.Fatal("expected HasMore=true")
	}
}

func TestPaginateLastPageNoMore(t *testing.T) {
	srv := startFakeBirdServer(t, buildRouteReply(15))
	page, err := paginate(context.Background(), srv.path, 5*time.Second, "show route protocol testproto", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tables[0].Routes) != 5 {
		t.Fatalf("got %d routes, want 5 (only 15 exist, offset 10)", len(page.Tables[0].Routes))
	}
	if page.HasMore {
		t.Fatal("expected HasMore=false at the true end")
	}
}

func TestPaginateOffsetPastEnd(t *testing.T) {
	srv := startFakeBirdServer(t, buildRouteReply(5))
	page, err := paginate(context.Background(), srv.path, 5*time.Second, "show route protocol testproto", 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tables) != 0 {
		t.Fatalf("expected no tables when offset is past the end, got %+v", page.Tables)
	}
	if page.HasMore {
		t.Fatal("expected HasMore=false")
	}
}

// TestStreamRoutesStopsEarly is the key bounded-memory guarantee: a huge
// reply (simulating a full-table peer) must not be fully read into memory
// just to serve a small early page. We assert this indirectly by using a
// reply large enough that a full parse would be slow/heavy, combined with a
// tight overall timeout — if streamRoutes were reading everything, this
// would either time out or take much longer than a bounded read should.
func TestStreamRoutesStopsEarly(t *testing.T) {
	srv := startFakeBirdServer(t, buildRouteReply(200000))
	start := time.Now()
	page, err := paginate(context.Background(), srv.path, 5*time.Second, "show route protocol testproto", 0, 3)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tables[0].Routes) != 3 {
		t.Fatalf("got %d routes, want 3", len(page.Tables[0].Routes))
	}
	if elapsed > 3*time.Second {
		t.Fatalf("paginate took %s for a 3-route page out of 200000 — looks like it read the whole reply", elapsed)
	}
}

func TestPaginateValidation(t *testing.T) {
	dir := t.TempDir()
	c := &Client{path: filepath.Join(dir, "no-such.sock"), timeout: time.Second}
	if _, err := c.RoutesByProtocolPage(context.Background(), "not a valid ident", false, 0, 10); err == nil {
		t.Fatal("expected identifier validation error")
	}
	if _, err := c.RoutesForPage(context.Background(), "not-a-prefix", false, 0, 10); err == nil {
		t.Fatal("expected prefix validation error")
	}
}
