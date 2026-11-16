package birdc

import (
	"bufio"
	"context"
	"errors"
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
	// Code is BIRD's terminal reply code. Most callers need only OK, but some
	// replies BIRD does not mark as errors still mean "nothing happened" — see
	// ConfigureConfirm.
	Code int
	// Failed names the protocols BIRD reported an error for inside an otherwise
	// accepted reply, e.g. "8006-edge1: reload failed". Only Reload fills it.
	Failed []string
}

// ErrNoReply marks a command that was written to BIRD but whose reply never
// arrived. Unlike a failure to connect or send, BIRD may have acted on it — for
// `configure timeout`, the new config may be running with the revert armed.
var ErrNoReply = errors.New("birdc: command sent, but BIRD's reply never arrived")

// Reply codes from BIRD's reconfigure commands that birdy acts on (they come
// from cmd_reconfig_msg in BIRD's nest/cmds.c; verified against BIRD 2.17.5).
const (
	CodeReconfigConfirmed = 18 // "Reconfiguration confirmed": an armed timeout was stopped
	CodeNothingToDo       = 19 // "Nothing to do": no armed reconfigure to confirm or undo
)

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
//
// Only CodeReconfigConfirmed is OK. With no timer armed BIRD answers
// CodeNothingToDo, which is not an error code — but it means BIRD is no longer
// running the armed config: the timeout already reverted it, BIRD restarted, or
// a plain `birdc configure` replaced it. Reporting that as a successful confirm
// would have the caller record a config as kept that BIRD is not running.
func (c *Client) ConfigureConfirm(ctx context.Context) (ConfigureResult, error) {
	res, err := c.configure(ctx, "configure confirm")
	if err == nil && res.Code != CodeReconfigConfirmed {
		res.OK = false
	}
	return res, err
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
// protocol ("8006-edge1: reload failed") and still reloads the rest, ending the
// reply with an ordinary 0000 — so OK only says BIRD took the command, and Failed
// names the protocols it could not reload. A direct protocol is always among
// them: it has nothing to re-import. The caller treats failures as a warning,
// not an apply failure — the config it applies is unaffected either way.
func (c *Client) Reload(ctx context.Context) (ConfigureResult, error) {
	reply, err := c.oneShot(ctx, "reload all")
	if err != nil {
		return ConfigureResult{}, err
	}
	res := configureResult(reply)
	for _, b := range reply.Blocks {
		if b.Code < 8000 || b.Code >= 10000 {
			continue
		}
		// BIRD folds consecutive lines with the same code into one block, so every
		// line of an error block is one protocol's "<name>: <reason>".
		for _, line := range b.Lines {
			if name, _, _ := strings.Cut(strings.TrimSpace(line), ":"); name != "" {
				res.Failed = append(res.Failed, name)
			}
		}
	}
	return res, nil
}

// configure runs one configure command and summarises BIRD's answer.
func (c *Client) configure(ctx context.Context, cmd string) (ConfigureResult, error) {
	reply, err := c.oneShot(ctx, cmd)
	if err != nil {
		return ConfigureResult{}, err
	}
	return configureResult(reply), nil
}

// oneShot runs one command on a fresh, disposable connection with a generous
// deadline — never the shared read connection, which uses a short timeout tuned
// for "show" queries. A failure after the command was written is wrapped in
// ErrNoReply, since BIRD may have acted on it.
func (c *Client) oneShot(ctx context.Context, cmd string) (Reply, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return Reply{}, ctxErr(ctx, fmt.Errorf("birdc: connect %s: %w", c.path, err))
	}
	defer conn.Close()
	// Cancelling ctx closes the socket, aborting a configure that hangs.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(deadlineFor(ctx, configureTimeout)); err != nil {
		return Reply{}, err
	}
	r := bufio.NewReader(conn)
	if _, err := readFrame(r); err != nil { // banner
		return Reply{}, ctxErr(ctx, fmt.Errorf("birdc: reading banner: %w", err))
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return Reply{}, ctxErr(ctx, fmt.Errorf("birdc: write: %w", err))
	}
	reply, err := readFrame(r)
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrNoReply, ctxErr(ctx, err))
	}
	return reply, nil
}

// configureResult joins every line BIRD returned into the operator-facing
// message, dropping the "Reading configuration from <path>" progress line — the
// path means nothing to the operator.
func configureResult(reply Reply) ConfigureResult {
	var msg []string
	for _, line := range append(reply.Lines(), reply.Terminal.Lines...) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Reading configuration from") {
			continue
		}
		msg = append(msg, line)
	}
	text := strings.Join(msg, "; ")

	code := reply.Terminal.Code
	if reply.IsError() {
		return ConfigureResult{OK: false, Message: text, Code: code}
	}
	if text == "" {
		text = "OK"
	}
	return ConfigureResult{OK: true, Message: text, Code: code}
}
