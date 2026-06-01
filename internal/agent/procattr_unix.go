//go:build !windows

package agent

import "syscall"

// newSysProcAttr returns a fresh SysProcAttr so each spawned process gets
// its own — the creack/pty library mutates SysProcAttr fields (Setsid,
// Setctty) in StartWithSize, so a shared package-level value would race
// across concurrent session starts.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
