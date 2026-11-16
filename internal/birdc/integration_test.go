//go:build integration

package birdc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// startBird runs a throwaway BIRD daemon in the foreground on a private socket
// and returns a client for it. It skips when bird is not installed or cannot
// start here (no privileges are needed for the protocols used).
func startBird(t *testing.T, conf string) *Client {
	t.Helper()
	bin, err := exec.LookPath("bird")
	if err != nil {
		t.Skip("bird not installed")
	}
	dir := t.TempDir()
	confPath := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "bird.ctl")
	cmd := exec.Command(bin, "-f", "-c", confPath, "-s", sock, "-P", filepath.Join(dir, "bird.pid"))
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start bird: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for range 50 {
		if c, err := Dial(sock, 2*time.Second); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skip("bird did not open its control socket")
	return nil
}

// The reply codes the apply pipeline relies on, against the real daemon: a
// confirm only succeeds while a timeout is armed, and BIRD's own revert leaves
// nothing to confirm; a reload lists the protocols it could not reload while
// still answering 0000, and a direct protocol is always among them.
func TestIntegrationConfigureRepliesFromBird(t *testing.T) {
	c := startBird(t, `router id 192.0.2.1;
protocol device {}
protocol direct direct1 { ipv4; }
protocol static static1 { ipv4; route 198.51.100.0/24 blackhole; }
`)
	ctx := context.Background()

	res, err := c.ConfigureConfirm(ctx)
	if err != nil || res.OK || res.Code != CodeNothingToDo {
		t.Fatalf("confirm with nothing armed = %+v, %v; want not OK, code %d", res, err, CodeNothingToDo)
	}

	if res, err := c.ConfigureTimeout(ctx, 30, true); err != nil || !res.OK {
		t.Fatalf("configure soft timeout = %+v, %v", res, err)
	}
	if res, err := c.ConfigureConfirm(ctx); err != nil || !res.OK || res.Code != CodeReconfigConfirmed {
		t.Fatalf("confirm while armed = %+v, %v; want OK, code %d", res, err, CodeReconfigConfirmed)
	}

	// BIRD's timer is running by the time it answers, so once the timeout has
	// passed (plus a little slack) counted from the answer, BIRD has reverted
	// and there is nothing left to confirm.
	if res, err := c.ConfigureTimeout(ctx, 1, true); err != nil || !res.OK {
		t.Fatalf("configure soft timeout 1 = %+v, %v", res, err)
	}
	time.Sleep(time.Second + 500*time.Millisecond)
	if res, err := c.ConfigureConfirm(ctx); err != nil || res.OK || res.Code != CodeNothingToDo {
		t.Fatalf("confirm after BIRD's revert = %+v, %v; want not OK, code %d", res, err, CodeNothingToDo)
	}

	res, err = c.Reload(ctx)
	if err != nil || !res.OK {
		t.Fatalf("reload = %+v, %v", res, err)
	}
	if !slices.Equal(res.Failed, []string{"direct1"}) {
		t.Fatalf("reload failures = %v, want [direct1]", res.Failed)
	}
}
