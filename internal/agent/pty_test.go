package agent

import (
	"os/exec"
	"testing"

	"github.com/creack/pty"
)

// requirePTY skips the test if PTY operations with process groups are
// not permitted (e.g. in sandboxed environments).
func requirePTY(t *testing.T) {
	t.Helper()
	cmd := exec.Command("true")
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("PTY not available: %v", err)
	}
	_ = ptmx.Close()
	_ = cmd.Wait()
}
