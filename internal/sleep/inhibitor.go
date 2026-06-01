// Package sleep prevents the host from going to sleep while Ideate has
// in-flight agent activity. Acquire holds an OS-level assertion; Release
// drops it. Implementations are GOOS-specific; non-darwin builds get a
// no-op so callers can stay platform-agnostic.
package sleep

// Inhibitor holds an OS sleep-prevention assertion for the duration
// between Acquire and Release. Implementations must be safe for
// concurrent calls; double-Acquire / double-Release are tolerated.
type Inhibitor interface {
	// Acquire ensures an assertion is held. The reason is surfaced to
	// the OS where supported (e.g. `pmset -g assertions` on macOS for
	// future cgo-based impls; ignored by the caffeinate subprocess
	// path).
	Acquire(reason string)
	// Release drops the assertion if held; no-op otherwise.
	Release()
	// Held reports whether an assertion is currently held.
	Held() bool
}

// Noop returns an Inhibitor that does nothing. Used on non-darwin
// builds and when the user has the feature disabled.
func Noop() Inhibitor { return noopInhibitor{} }

type noopInhibitor struct{}

func (noopInhibitor) Acquire(string) {}
func (noopInhibitor) Release()       {}
func (noopInhibitor) Held() bool     { return false }
