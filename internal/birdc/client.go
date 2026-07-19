// Package birdc implements a client for the BIRD 2.x control socket, the same
// line-based protocol birdc speaks. Wire format (verified against a live
// BIRD 2.17.1 instance): each line is either
//
//	NNNN<sep><text>   where NNNN is a 4-digit code and sep is one of ' ' '-' '+'
//	<space><text>     a continuation line belonging to the current block
//
// '-' means more lines follow; ' ' terminates the whole reply; '+' is an
// async/out-of-band line (e.g. log output) and does not end the reply.
package birdc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Block is one code-tagged section of a reply: an opening coded line plus any
// continuation lines that followed it before the next coded line arrived.
type Block struct {
	Code  int
	Lines []string
}

// Reply is the full response to one command.
type Reply struct {
	Blocks   []Block
	Terminal Block // the final, space-terminated line
}

// Lines returns every text line from every block, in order, flattened.
func (r Reply) Lines() []string {
	var out []string
	for _, b := range r.Blocks {
		out = append(out, b.Lines...)
	}
	return out
}

// IsError reports whether the terminal code indicates a BIRD-side error
// (BIRD uses 8xxx/9xxx for errors; anything else is a normal reply code).
func (r Reply) IsError() bool {
	return r.Terminal.Code >= 8000 && r.Terminal.Code < 10000
}

// Client is a connection to a BIRD control socket. Safe for concurrent use;
// commands are serialized internally since the socket is a single request/
// response stream.
type Client struct {
	path    string
	timeout time.Duration

	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to the BIRD control socket at path and reads the welcome banner.
func Dial(path string, timeout time.Duration) (*Client, error) {
	c := &Client{path: path, timeout: timeout}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) connect() error {
	conn, err := net.DialTimeout("unix", c.path, c.timeout)
	if err != nil {
		return fmt.Errorf("birdc: connect %s: %w", c.path, err)
	}
	c.conn = conn
	c.r = bufio.NewReader(conn)
	// Consume the welcome banner (code 0001).
	if _, err := readFrame(c.r); err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("birdc: reading banner: %w", err)
	}
	return nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Command sends a single-line command and returns the parsed reply. On
// connection errors it transparently reconnects once and retries. The context
// bounds the command: its cancellation or deadline aborts a blocked socket
// read, but never triggers the reconnect-retry (that is for a dropped socket).
func (c *Client) Command(ctx context.Context, cmd string) (Reply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.commandLocked(ctx, cmd)
	if err != nil && c.conn == nil && ctx.Err() == nil {
		// connection was dropped; try once more after reconnecting
		if cerr := c.connect(); cerr == nil {
			reply, err = c.commandLocked(ctx, cmd)
		}
	}
	return reply, err
}

func (c *Client) commandLocked(ctx context.Context, cmd string) (Reply, error) {
	if c.conn == nil {
		return Reply{}, fmt.Errorf("birdc: not connected")
	}
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	// Capture the connection in a local: the AfterFunc callback runs on another
	// goroutine, so it must not touch the c.conn field this method may nil out on
	// error. Closing the captured conn is safe and idempotent, and unblocks any
	// in-flight read/write when ctx is cancelled.
	conn := c.conn
	if err := conn.SetDeadline(deadlineFor(ctx, c.timeout)); err != nil {
		return Reply{}, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if strings.ContainsAny(cmd, "\r\n") {
		return Reply{}, fmt.Errorf("birdc: command must not contain newlines")
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		conn.Close()
		c.conn = nil
		return Reply{}, ctxErr(ctx, fmt.Errorf("birdc: write: %w", err))
	}
	reply, err := readFrame(c.r)
	if err != nil {
		conn.Close()
		c.conn = nil
		return Reply{}, ctxErr(ctx, err)
	}
	return reply, nil
}

// deadlineFor returns the sooner of now+timeout and ctx's own deadline, so a
// socket op is bounded by whichever expires first.
func deadlineFor(ctx context.Context, timeout time.Duration) time.Time {
	dl := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		return d
	}
	return dl
}

// ctxErr prefers the context's error when it is done, so a cancelled or
// timed-out command reports context.Canceled/DeadlineExceeded rather than the
// incidental "use of closed network connection" the close produced.
func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// readFrame reads coded/continuation lines from r until a space-terminated
// line closes the reply. Split out from Client so the wire framing can be
// unit tested against captured raw bytes without a real socket.
func readFrame(r *bufio.Reader) (Reply, error) {
	var reply Reply
	var cur *Block

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return reply, fmt.Errorf("birdc: read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		code, sep, text, coded := parseCodedLine(line)
		if !coded {
			// continuation line: strip exactly one leading marker space, if present
			text := strings.TrimPrefix(line, " ")
			if cur != nil {
				cur.Lines = append(cur.Lines, text)
			}
			continue
		}

		switch sep {
		case '+':
			// async/out-of-band notification; does not affect reply framing
			continue
		case '-':
			reply.Blocks = append(reply.Blocks, Block{Code: code, Lines: []string{text}})
			cur = &reply.Blocks[len(reply.Blocks)-1]
		case ' ':
			reply.Terminal = Block{Code: code, Lines: []string{text}}
			return reply, nil
		}
	}
}

// parseCodedLine reports whether line begins with "NNNN" followed by one of
// ' ', '-', '+' and, if so, returns the code, separator and remaining text.
func parseCodedLine(line string) (code int, sep byte, text string, ok bool) {
	if len(line) < 5 {
		return 0, 0, "", false
	}
	for i := range 4 {
		if line[i] < '0' || line[i] > '9' {
			return 0, 0, "", false
		}
	}
	s := line[4]
	if s != ' ' && s != '-' && s != '+' {
		return 0, 0, "", false
	}
	code = int(line[0]-'0')*1000 + int(line[1]-'0')*100 + int(line[2]-'0')*10 + int(line[3]-'0')
	return code, s, line[5:], true
}
