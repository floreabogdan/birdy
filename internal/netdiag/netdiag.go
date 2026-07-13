// Package netdiag runs read-only reachability diagnostics — ping and traceroute
// — from the router, for the looking glass. It execs external tools, so birdy
// only offers it when the operator opts in (--netdiag), and it validates the
// target to a plain IP or hostname before it ever reaches a command line.
package netdiag

import (
	"context"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Tool is a supported diagnostic.
type Tool string

const (
	Ping       Tool = "ping"
	Traceroute Tool = "traceroute"
)

// Tools is the set offered in the UI, in display order.
var Tools = []Tool{Ping, Traceroute}

// runTimeout bounds any single diagnostic; the per-tool flags aim well under it,
// this is the backstop.
const runTimeout = 45 * time.Second

// maxOutput caps the captured output. ping/traceroute output is small; this only
// guards against a pathological case.
const maxOutput = 64 << 10

// Result is the outcome of one run.
type Result struct {
	Command string // the exact argv run, for transparency
	Output  string
	// Err is a run error (the tool exited non-zero, timed out). ping/traceroute
	// exit non-zero on an unreachable target but still print useful output, so
	// Output is the thing to show; Err is secondary.
	Err string
	// Skipped is set when the tool is not installed, so the UI can guide rather
	// than error.
	Skipped string
}

// AvailableTools lists the diagnostics this router actually has installed. A
// container image or a minimal router build may carry neither, which is why the
// feature is detected rather than assumed.
func AvailableTools() []Tool {
	var out []Tool
	for _, t := range Tools {
		bin, _, ok := argv(t, "127.0.0.1")
		if !ok {
			continue
		}
		if _, err := exec.LookPath(bin); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// Available reports whether at least one diagnostic can run.
func Available() bool { return len(AvailableTools()) > 0 }

// hostRe bounds a hostname: it must start and end alphanumeric and hold only
// letters, digits, dots and hyphens. This — plus the IP check — guarantees the
// target can never start with '-' (and so be read as a flag) or carry a shell
// metacharacter, independent of the fact that exec runs no shell.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,253}[A-Za-z0-9])?$`)

// ValidTarget reports whether target is a usable IP address or hostname.
func ValidTarget(target string) bool {
	target = strings.TrimSpace(target)
	if _, err := netip.ParseAddr(target); err == nil {
		return true
	}
	return hostRe.MatchString(target)
}

// argv builds the binary and arguments for a tool and target. -n keeps output
// numeric (no reverse-DNS stalls); the count/wait/hop caps keep a run short.
func argv(tool Tool, target string) (string, []string, bool) {
	switch tool {
	case Ping:
		return "ping", []string{"-n", "-c", "4", "-w", "10", target}, true
	case Traceroute:
		return "traceroute", []string{"-n", "-q", "1", "-w", "2", "-m", "20", target}, true
	}
	return "", nil, false
}

// Run executes a diagnostic against target with a bounded timeout, returning
// whatever the tool printed. It refuses an invalid target and reports a missing
// tool as Skipped rather than an error.
func Run(ctx context.Context, tool Tool, target string) Result {
	target = strings.TrimSpace(target)
	if !ValidTarget(target) {
		return Result{Err: "Enter a valid IP address or hostname."}
	}
	bin, args, ok := argv(tool, target)
	if !ok {
		return Result{Err: "Unknown diagnostic."}
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return Result{Skipped: bin + " is not installed on the router."}
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, path, args...).CombinedOutput()

	res := Result{Command: bin + " " + strings.Join(args, " "), Output: truncate(string(out))}
	if runErr != nil {
		res.Err = runErr.Error()
	}
	return res
}

func truncate(s string) string {
	if len(s) > maxOutput {
		return s[:maxOutput] + "\n… (truncated)"
	}
	return s
}
