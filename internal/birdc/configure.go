package birdc

import (
	"bufio"
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
func (c *Client) ConfigureCheck() (ConfigureResult, error) {
	return c.configure("configure check")
}

// ConfigureTimeout applies the config file with an armed auto-revert: if it is
// not confirmed within seconds, BIRD reverts to the previous config on its own.
// This is what makes reconfiguring a remote router safe — lose the session, or
// break your own reachability, and the router heals itself.
func (c *Client) ConfigureTimeout(seconds int) (ConfigureResult, error) {
	return c.configure(fmt.Sprintf("configure timeout %d", seconds))
}

// ConfigureConfirm keeps a timeout-armed reconfigure that would otherwise revert.
func (c *Client) ConfigureConfirm() (ConfigureResult, error) {
	return c.configure("configure confirm")
}

// ConfigureUndo reverts the last reconfigure immediately, without waiting for
// the timeout to elapse.
func (c *Client) ConfigureUndo() (ConfigureResult, error) {
	return c.configure("configure undo")
}

// configure runs one configure command on a fresh, disposable connection with a
// generous deadline — never the shared read connection, which uses a short
// timeout tuned for "show" queries.
func (c *Client) configure(cmd string) (ConfigureResult, error) {
	conn, err := net.DialTimeout("unix", c.path, configureTimeout)
	if err != nil {
		return ConfigureResult{}, fmt.Errorf("birdc: connect %s: %w", c.path, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(configureTimeout)); err != nil {
		return ConfigureResult{}, err
	}
	r := bufio.NewReader(conn)
	if _, err := readFrame(r); err != nil { // banner
		return ConfigureResult{}, fmt.Errorf("birdc: reading banner: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return ConfigureResult{}, fmt.Errorf("birdc: write: %w", err)
	}
	reply, err := readFrame(r)
	if err != nil {
		return ConfigureResult{}, err
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
