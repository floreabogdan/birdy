package birdc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// RoutePage is one page of a route listing. Tables preserves BIRD's table
// grouping across whatever entries landed on this page. HasMore is true when
// at least one more matching entry exists beyond this page.
type RoutePage struct {
	Tables  []RouteTable
	HasMore bool
}

// streamRoutes runs cmd on a fresh, disposable connection (never the shared
// long-lived Client connection) and calls fn for each fully-parsed route
// entry in order. As soon as fn returns false, streamRoutes stops reading
// and closes the connection immediately — it does not drain the rest of
// BIRD's reply. This is what keeps pagination bounded in memory: querying
// page 1 of a peer carrying a full table never requires holding, or even
// receiving, the other 2 million entries.
func streamRoutes(ctx context.Context, socketPath string, timeout time.Duration, cmd string, fn func(table string, entry RouteEntry) bool) error {
	if strings.ContainsAny(cmd, "\r\n") {
		return fmt.Errorf("birdc: command must not contain newlines")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("birdc: connect %s: %w", socketPath, err))
	}
	defer conn.Close()
	// Cancelling ctx closes the socket, unblocking the read below on a deep page.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(deadlineFor(ctx, timeout)); err != nil {
		return err
	}
	r := bufio.NewReader(conn)

	if _, err := readFrame(r); err != nil { // banner
		return fmt.Errorf("birdc: reading banner: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return fmt.Errorf("birdc: write: %w", err)
	}

	table := ""
	lastNetwork := ""
	var pending *RouteEntry
	pendingTable := ""

	// flush emits the buffered entry (if any) to fn. One-entry lookahead is
	// needed because a route's "via"/"dev"/attr detail lines arrive AFTER
	// the entry's own line, so an entry isn't complete until the next
	// boundary (a new entry, a new table, or end of reply) is seen.
	flush := func() bool {
		if pending == nil {
			return true
		}
		ok := fn(pendingTable, *pending)
		pending = nil
		return ok
	}

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("birdc: read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		code, sep, text, coded := parseCodedLine(line)
		var content string
		if coded {
			if sep == '+' {
				continue
			}
			content = text
		} else {
			content = strings.TrimPrefix(line, " ")
		}

		if coded && sep == ' ' {
			if code >= 8000 && code < 10000 {
				return fmt.Errorf("birdc: %s", content)
			}
			flush()
			return nil
		}

		trimmed := strings.TrimRight(content, " ")
		switch {
		case strings.TrimSpace(trimmed) == "":
			// blank continuation line
		case strings.HasPrefix(trimmed, "Table ") && strings.HasSuffix(strings.TrimSpace(trimmed), ":"):
			if !flush() {
				return nil
			}
			table = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(trimmed, "Table ")), ":")
			lastNetwork = ""
		case strings.HasPrefix(trimmed, "\t"):
			attachRouteDetail(pending, trimmed)
		default:
			if entry, network, ok := parseRouteLine(trimmed, lastNetwork); ok {
				if !flush() {
					return nil
				}
				pending = &entry
				pendingTable = table
				lastNetwork = network
			} else {
				attachRouteDetail(pending, trimmed)
			}
		}
	}
}

// paginate collects one page of results from streamRoutes, skipping the
// first offset matching entries (still parsed, since BIRD's CLI has no
// server-side offset — but never accumulated) and gathering up to limit
// after that. It reads exactly one entry past the page to learn HasMore,
// then stops.
func paginate(ctx context.Context, socketPath string, timeout time.Duration, cmd string, offset, limit int) (RoutePage, error) {
	if limit <= 0 {
		limit = 50
	}
	var page RoutePage
	tables := map[string]*RouteTable{}
	var order []string
	seen := 0

	err := streamRoutes(ctx, socketPath, timeout, cmd, func(table string, entry RouteEntry) bool {
		seen++
		if seen <= offset {
			return true
		}
		if seen > offset+limit {
			page.HasMore = true
			return false
		}
		t, ok := tables[table]
		if !ok {
			t = &RouteTable{Name: table}
			tables[table] = t
			order = append(order, table)
		}
		t.Routes = append(t.Routes, entry)
		return true
	})
	if err != nil {
		return RoutePage{}, err
	}
	for _, name := range order {
		page.Tables = append(page.Tables, *tables[name])
	}
	return page, nil
}
