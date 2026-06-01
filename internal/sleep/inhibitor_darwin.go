package sleep

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// New returns the platform inhibitor for darwin: a wrapper around
// `caffeinate -di -w <pid>` so the assertion auto-releases if Ideate
// crashes (the child wakes from waitpid and exits).
//
//   - -d  block display sleep (so the user can monitor a running tool
//     call without the screen dimming)
//   - -i  block idle-system sleep
//   - -w  tie the child's lifetime to the given pid
func New() Inhibitor {
	return &caffeinateInhibitor{pid: strconv.Itoa(os.Getpid())}
}

type caffeinateInhibitor struct {
	pid string

	mu  sync.Mutex
	cmd *exec.Cmd
}

func (c *caffeinateInhibitor) Acquire(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return
	}
	cmd := exec.Command("/usr/bin/caffeinate", "-di", "-w", c.pid)
	if err := cmd.Start(); err != nil {
		slog.Warn("sleep: caffeinate start failed",
			slog.String("reason", reason), slog.Any("err", err))
		return
	}
	c.cmd = cmd
	slog.Info("sleep: assertion acquired",
		slog.String("reason", reason),
		slog.Int("caffeinate_pid", cmd.Process.Pid))
	go func(cmd *exec.Cmd) {
		// Reap the child so it doesn't zombie. Wait blocks until the
		// child exits (which happens when our pid dies, see -w).
		_ = cmd.Wait()
	}(cmd)
}

func (c *caffeinateInhibitor) Release() {
	c.mu.Lock()
	cmd := c.cmd
	c.cmd = nil
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		slog.Warn("sleep: caffeinate kill failed",
			slog.Int("caffeinate_pid", cmd.Process.Pid), slog.Any("err", err))
	}
}

func (c *caffeinateInhibitor) Held() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}
