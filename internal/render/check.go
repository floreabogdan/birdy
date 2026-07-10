package render

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckResult is the verdict of `bird -p` on a candidate config.
type CheckResult struct {
	OK      bool
	Output  string // bird's own stdout+stderr, verbatim
	Skipped string // non-empty when the check could not run at all
}

// Check writes cfg to a temporary file and asks BIRD to parse it. This is a
// pure syntax and semantics check: `bird -p` never touches the running daemon,
// never opens the control socket, and never reads the live config.
//
// birdBinary may be a bare name resolved via PATH ("bird") or an absolute path.
// A config that fails here must never reach disk.
func Check(ctx context.Context, birdBinary, cfg string) CheckResult {
	if birdBinary == "" {
		birdBinary = "bird"
	}
	bin, err := exec.LookPath(birdBinary)
	if err != nil {
		return CheckResult{Skipped: "bird binary not found: " + err.Error()}
	}

	dir, err := os.MkdirTemp("", "birdy-check-")
	if err != nil {
		return CheckResult{Skipped: "could not create temp dir: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	// 0600: the candidate config can contain BGP session passwords.
	path := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		return CheckResult{Skipped: "could not write temp config: " + err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "-p", "-c", path).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CheckResult{Skipped: "bird -p timed out"}
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		// Never leak the temp path into the UI; it means nothing to the user.
		return CheckResult{OK: false, Output: strings.ReplaceAll(text, path, "bird.conf")}
	}
	if text == "" {
		text = "Configuration OK"
	}
	return CheckResult{OK: true, Output: strings.ReplaceAll(text, path, "bird.conf")}
}
