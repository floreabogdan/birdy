package birdc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// configureTimeout is generous: a reconfigure that restarts protocols can take
// several seconds, well past the Client's usual short command deadline.
const configureTimeout = 30 * time.Second

// ConfigureResult is the outcome of a configure command. Message is BIRD's own
// text, joined — shown to the operator verbatim.
type ConfigureResult struct {
	OK      bool
	Message string
}

// The configure commands all act on the daemon's own configured config file —
// the path BIRD was started with (-c), which birdy must write to. BIRD's CLI
// accepts a file argument on `configure check` but NOT on `configure timeout`
// (verified against BIRD 2.17.1: "configure timeout 30 \"file\"" is a syntax
// error). So none of these pass a path: birdy writes the file first, then asks
// BIRD to load it. This is why birdy's --bird-conf must match BIRD's own -c.

// ConfigureCheck asks the running daemon to parse its config file without
// applying it. It is the socket twin of `bird -p`: it never changes the running
// config, and it validates against the exact daemon that will load the file.
func (c *Client) ConfigureCheck(ctx context.Context) (ConfigureResult, error) {
	return c.configure(ctx, "configure check")
}

// ConfigureTimeout applies the config file with an armed auto-revert: if it is
// not confirmed within seconds, BIRD reverts to the previous config on its own.
// This is what makes reconfiguring a remote router safe — lose the session, or
// break your own reachability, and the router heals itself.
//
// soft reloads filters and re-runs them against existing routes without
// restarting protocols, so a BGP session is not bounced for a policy change.
// BIRD still restarts a protocol whose core parameters changed; soft only avoids
// the restart where it safely can.
func (c *Client) ConfigureTimeout(ctx context.Context, seconds int, soft bool) (ConfigureResult, error) {
	verb := "configure"
	if soft {
		verb += " soft"
	}
	return c.configure(ctx, fmt.Sprintf("%s timeout %d", verb, seconds))
}

// ConfigureConfirm keeps a timeout-armed reconfigure that would otherwise revert.
func (c *Client) ConfigureConfirm(ctx context.Context) (ConfigureResult, error) {
	return c.configure(ctx, "configure confirm")
}

// DaemonConfigPath returns the file BIRD reads its configuration from, parsed
// from the "Reading configuration from <path>" line a `configure check` emits.
// The check never applies anything, so this is a safe probe. birdy uses it to
// confirm --bird-conf points at the same file the daemon loads — otherwise an
// apply would reconfigure the wrong file.
func (c *Client) DaemonConfigPath(ctx context.Context) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return "", ctxErr(ctx, fmt.Errorf("birdc: connect %s: %w", c.path, err))
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(deadlineFor(ctx, configureTimeout)); err != nil {
		return "", err
	}
	r := bufio.NewReader(conn)
	if _, err := readFrame(r); err != nil {
		return "", fmt.Errorf("birdc: reading banner: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "configure check\n"); err != nil {
		return "", fmt.Errorf("birdc: write: %w", err)
	}
	reply, err := readFrame(r)
	if err != nil {
		return "", err
	}
	const marker = "Reading configuration from "
	for _, line := range append(reply.Lines(), reply.Terminal.Lines...) {
		if _, after, found := strings.Cut(line, marker); found {
			return strings.TrimSpace(after), nil
		}
	}
	return "", fmt.Errorf("birdc: could not determine config path from BIRD")
}

// ConfigureUndo reverts the last reconfigure immediately, without waiting for
// the timeout to elapse.
func (c *Client) ConfigureUndo(ctx context.Context) (ConfigureResult, error) {
	return c.configure(ctx, "configure undo")
}

// Reload re-runs every protocol's import and export against the routes already
// in the table: it re-imports (asking BGP peers for a route refresh) and
// re-exports preferred routes. A reconfigure alone does not do this — a soft one
// especially, which by design keeps propagating whatever the old filters already
// accepted and only applies the new filters to routes that arrive afterward. So
// a soft reconfigure is paired with a reload to make a policy or prefix change
// actually take effect on the current table, without restarting — and so without
// bouncing — any session.
//
// It is best-effort: re-import needs the peer's route-refresh capability, and a
// peer that lacks it cannot be refreshed without a restart. BIRD reports that per
// protocol and still reloads the rest, so the caller treats a non-OK result as a
// warning, not a failure — the config it applies is unaffected either way.
func (c *Client) Reload(ctx context.Context) (ConfigureResult, error) {
	return c.configure(ctx, "reload all")
}

// configure runs one configure command on a fresh, disposable connection with a
// generous deadline — never the shared read connection, which uses a short
// timeout tuned for "show" queries.
func (c *Client) configure(ctx context.Context, cmd string) (ConfigureResult, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return ConfigureResult{}, ctxErr(ctx, fmt.Errorf("birdc: connect %s: %w", c.path, err))
	}
	defer conn.Close()
	// Cancelling ctx closes the socket, aborting a configure that hangs.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(deadlineFor(ctx, configureTimeout)); err != nil {
		return ConfigureResult{}, err
	}
	r := bufio.NewReader(conn)
	if _, err := readFrame(r); err != nil { // banner
		return ConfigureResult{}, ctxErr(ctx, fmt.Errorf("birdc: reading banner: %w", err))
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return ConfigureResult{}, ctxErr(ctx, fmt.Errorf("birdc: write: %w", err))
	}
	reply, err := readFrame(r)
	if err != nil {
		return ConfigureResult{}, ctxErr(ctx, err)
	}

	// Join every line BIRD returned, dropping the "Reading configuration from
	// <path>" progress line — the path means nothing to the operator.
	var msg []string
	for _, line := range append(reply.Lines(), reply.Terminal.Lines...) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Reading configuration from") {
			continue
		}
		msg = append(msg, line)
	}
	text := strings.Join(msg, "; ")

	if reply.IsError() {
		return ConfigureResult{OK: false, Message: text}, nil
	}
	if text == "" {
		text = "OK"
	}
	return ConfigureResult{OK: true, Message: text}, nil
}
