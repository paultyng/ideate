// Package vscreen wraps a [vt.Emulator] with a mutex so PTY drain goroutines
// can feed bytes concurrently with replay-snapshot reads from binding calls.
//
// Each session gets one [Buffer]. The PTY drain loop calls [Buffer.Feed] for
// every chunk; the binding's replay path calls [Buffer.Snapshot] to get a
// rendered ANSI string of the current screen state (including SGR + OSC 8
// links) suitable for writing back into xterm.js on a fresh mount.
//
// This replaces the prior 4 MB byte ring: state size is bounded by
// rows × cols (plus scrollback), not by total bytes ever written, so long-
// running sessions and route switches both replay cleanly.
package vscreen

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
)

// snapshotChunkThreshold is the number of Feed chunks between periodic
// disk writes. Chosen to balance write amplification (one write per 500
// chunks) against the worst-case lost output if the process dies between
// writes. At typical 4 KB chunks this is ~2 MB of PTY output between writes.
const snapshotChunkThreshold = 500

// snapshotTimeThreshold is the wall-clock interval between periodic
// disk writes. Ensures a flush even when traffic is bursty then quiet
// (e.g. 100 chunks then silence for 60 s — the 30 s threshold still fires
// on the next Feed call after the deadline passes).
const snapshotTimeThreshold = 30 * time.Second

// Default scrollback. Was xterm.js's 1000-line default; bumped to
// 10000 so users see meaningful backscroll after a session toggle and
// `mcp__ideate__get_session_output` returns more than the most recent
// screenful for long-running agents. Memory cost at 80×10000 is
// roughly 12MB per session, ~24MB at 180×10000 — still well under the
// prior 4MB-byte-ring-per-session cost we shipped before vscreen.
const defaultScrollbackLines = 10000

// scrollbackEnvVar lets power users dial the scrollback up or down
// without rebuilding. Read at Buffer construction; bad values fall
// back to the default.
const scrollbackEnvVar = "IDEATE_VSCREEN_SCROLLBACK"

// Bounds clamp the env-overridden scrollback. The lower bound keeps
// the snapshot useful (a few screenfuls); the upper bound caps memory
// growth at ~250MB per session at 180 cols.
const (
	minScrollbackLines = 100
	maxScrollbackLines = 100000
)

// scrollbackSize returns the scrollback line count, honoring
// IDEATE_VSCREEN_SCROLLBACK when set to a parseable integer in
// [minScrollbackLines, maxScrollbackLines].
func scrollbackSize() int {
	raw := os.Getenv(scrollbackEnvVar)
	if raw == "" {
		return defaultScrollbackLines
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return defaultScrollbackLines
	}
	if v < minScrollbackLines {
		return minScrollbackLines
	}
	if v > maxScrollbackLines {
		return maxScrollbackLines
	}
	return v
}

// Buffer is a per-session emulator wrapper.
type Buffer struct {
	mu     sync.Mutex
	emu    *vt.Emulator
	closed bool // guarded by mu; makes Close idempotent

	// snapshotPath is the target file for periodic + on-close disk
	// persistence. Empty means no persistence. Injected via
	// SetSnapshotPath from the session construction site.
	snapshotPath string

	// chunksSinceWrite counts Feed calls since the last disk write.
	// Reset on each successful write.
	chunksSinceWrite int

	// lastWriteAt is the wall-clock time of the last successful disk write.
	// Reset on each successful write.
	lastWriteAt time.Time

	// clock returns the current time. Overridden in tests for
	// deterministic time-threshold assertions; production uses time.Now.
	clock func() time.Time
}

// New constructs a buffer at the given column/row dimensions.
//
// vt.Emulator's Write path runs response handlers (DA1, cursor-position
// reports, etc.) that push bytes into an internal unbuffered io.Pipe.
// Those responses are meant for the upstream agent, but in Ideate the
// frontend xterm.js already handles terminal-query responses via PTY
// stdin. If nobody drains the pipe, the first response write blocks
// forever inside Feed and the entire drain goroutine deadlocks — frontend
// renders no output at all. So we spawn a goroutine that reads-and-
// discards the emulator's input pipe for the lifetime of the Buffer.
func New(cols, rows int) *Buffer {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	emu := vt.NewEmulator(cols, rows)
	emu.SetScrollbackSize(scrollbackSize())
	b := &Buffer{
		emu:         emu,
		clock:       time.Now,
		lastWriteAt: time.Now(),
	}
	go func() {
		_, _ = io.Copy(io.Discard, emu)
	}()
	return b
}

// SetSnapshotPath sets the file path for periodic and on-close disk
// persistence. Call immediately after New, before the first Feed, from
// the session construction site that already knows the idea directory.
// An empty path disables persistence (the default).
func (b *Buffer) SetSnapshotPath(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshotPath = path
	b.lastWriteAt = b.clock()
}

// Feed writes PTY bytes into the emulator. Safe to call from the drain
// goroutine while other callers read [Buffer.Snapshot].
//
// When a snapshotPath is set, Feed also checks the periodic-write
// thresholds (500 chunks or 30 s since last write) and flushes to disk
// when either fires. The write is inline — no background goroutine.
func (b *Buffer) Feed(p []byte) {
	if len(p) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = b.emu.Write(p)

	if b.snapshotPath == "" {
		return
	}
	b.chunksSinceWrite++
	now := b.clock()
	if b.chunksSinceWrite >= snapshotChunkThreshold || now.Sub(b.lastWriteAt) >= snapshotTimeThreshold {
		b.writeSnapshotLocked()
	}
}

// writeSnapshotLocked writes the current snapshot to snapshotPath using an
// atomic temp+rename. Must be called with b.mu held. Resets chunksSinceWrite
// and lastWriteAt on success; on failure it only logs (best-effort).
//
// The raw renderNoLock() bytes are written as-is; fixOSC8Swap is applied on
// read (ReadSnapshot) and on in-process Snapshot(), matching how the live
// terminal-render path works.
func (b *Buffer) writeSnapshotLocked() {
	if b.snapshotPath == "" {
		return
	}
	data := b.renderNoLock()
	path := b.snapshotPath
	b.chunksSinceWrite = 0
	b.lastWriteAt = b.clock()
	b.mu.Unlock()
	_ = atomicWriteFile(path, data) // best-effort; failure doesn't disrupt PTY drain
	b.mu.Lock()
}

// atomicWriteFile writes data to path via a temp file in the same
// directory, then renames it into place. The parent directory must
// already exist. This avoids torn reads if the process dies between
// the write and rename.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating snapshot dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".snap-*")
	if err != nil {
		return fmt.Errorf("creating temp snapshot: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp snapshot: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming temp snapshot: %w", err)
	}
	return nil
}

// Snapshot returns the current screen state serialized as ANSI bytes.
// The result is suitable for writing into a fresh xterm.js instance to
// reproduce the visual state at the moment of the call — including
// scrollback. xterm processes the byte stream line-by-line: scrollback
// lines we emit first are pushed into xterm's own scrollback as later
// rows scroll them off the visible window, so the user can scroll up
// in the re-mounted terminal to see history beyond the visible
// screen.
//
// vt.Emulator.Render() only renders the visible screen, so we
// concatenate the rendered scrollback ahead of it. vt.Render() (and
// uv.Line.Render()) separate lines with bare LF, but xterm.js (with
// LNM off, the default) treats LF as cursor-down-only — each
// subsequent line would start at the previous line's end column,
// producing a staircased "wrapping offset" pattern. Convert to CRLF
// so every line starts at column 0.
func (b *Buffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fixOSC8Swap(b.renderNoLock())
}

// renderNoLock produces the ANSI snapshot bytes from the emulator's
// current state. Must be called with b.mu already held.
//
// The alt-screen replay path (DECSET ?1049 enter + cursor restore)
// was removed in 2026-06: no agent we run uses alt-screen any more
// (testagent v0.4+ uses bubbletea inline mode; Claude Code v2.1+
// emits zero ?1049h/?1049l, verified by direct PTY probe). The
// emu.IsAltScreen() accessor is kept available in case a future
// agent reintroduces alt-screen mode; the snapshot path was the
// only caller.
func (b *Buffer) renderNoLock() []byte {
	var buf bytes.Buffer
	if sb := b.emu.Scrollback(); sb != nil {
		for _, line := range sb.Lines() {
			buf.WriteString(line.Render())
			buf.WriteString("\r\n")
		}
	}
	buf.Write(bytes.ReplaceAll([]byte(b.emu.Render()), []byte("\n"), []byte("\r\n")))
	return buf.Bytes()
}

// Resize updates the emulator's dimensions. Should be called whenever the
// PTY is resized so cursor positioning and line-wrap behavior in the
// snapshot match what the live xterm renders.
func (b *Buffer) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emu.Resize(cols, rows)
}

// CellContentAt returns the visible-screen cell content at the given
// (x, y) position. Test affordance — production callers go through
// Snapshot. Returns "" if outside the grid.
func (b *Buffer) CellContentAt(x, y int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.emu.CellAt(x, y)
	if c == nil {
		return ""
	}
	return c.Content
}

// Dimensions returns the current width/height of the visible screen.
// Test affordance.
func (b *Buffer) Dimensions() (cols, rows int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emu.Width(), b.emu.Height()
}

// Close releases emulator resources.
//
// Known race: vt.Emulator's `e.closed` field is unsynchronized
// between Read (emulator.go:252) and Close (emulator.go:265), and
// the drain goroutine in New() reads `e.closed` while this Close
// writes it. The race detector flags this ~2% of the time, surfacing
// most often as a flake in `TestCoordinator_Write`.
//
// Upstream is aware: charmbracelet/x#850 attempted a fix but was
// reverted 26 minutes later by #851 because the same change "causes
// data races, despite tests looking like they pass", and even a
// follow-up `atomic.Bool` attempt hit "various other places where
// there are data races". The maintainer punted.
//
// Local workarounds (waiting on a drain channel after Close; using
// a DA1 query to wake the parked Read before Close; making Close a
// no-op and leaking the goroutine) each have their own side effects
// — see the discussion archived in the original vscreen race plan.
// We accept the low-frequency flake here rather than fight upstream.
// Close persists the final snapshot and releases emulator resources.
// Safe to call more than once; only the first call does work.
func (b *Buffer) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	// Persist the final snapshot before closing the emulator so
	// Task 3a's dormant-session view has something to render even
	// when the session died unexpectedly (no graceful app shutdown).
	b.writeSnapshotLocked() // may unlock+relock internally
	_ = b.emu.Close()
	b.mu.Unlock()
}

// ReadSnapshot reads the persisted snapshot file at path and applies
// fixOSC8Swap to produce bytes suitable for streaming into xterm.js.
// Returns (nil, nil) when the file does not exist.
func ReadSnapshot(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading snapshot: %w", err)
	}
	return fixOSC8Swap(data), nil
}

// osc8SwapRE matches a fully-formed OSC 8 sequence emitted by ultraviolet's
// Buffer.Render(): ESC ] 8 ; <first> ; <second> ST, where ST is BEL (\x07)
// or ESC \. Captures the two semicolon-separated slots so fixOSC8Swap can
// swap them back into spec order.
//
// Charset note: per OSC 8 spec, params can contain '=' and ':'; URIs are
// percent-encoded so semicolons inside the URI are %3B. Splitting on the
// first two semicolons after the '8;' opener is therefore unambiguous.
var osc8SwapRE = regexp.MustCompile("\x1b\\]8;([^;]*);([^\x07\x1b]*)(\x07|\x1b\\\\)")

// fixOSC8Swap reverses the URL/Params swap introduced by
// charmbracelet/x/vt's handleHyperlink. The upstream parser stores
// parts[1] (the params slot in ESC ] 8 ; <params> ; <uri> ST) into
// Link.URL and parts[2] (the uri slot) into Link.Params; when
// ultraviolet later renders the buffer via ansi.SetHyperlink(URL,
// Params), the emitted bytes have the two fields in the OPPOSITE
// slots from what they came in as. The common case — input
// "ESC]8;;<uri>ESC\" with no params — yields output "ESC]8;<uri>;BEL"
// (URI in the params slot, URI slot empty), which xterm.js treats
// as a link with empty URI: visible styling but no hover, no click.
//
// We undo the swap by re-emitting every OSC 8 open sequence with
// the two slots flipped. The close sequence "ESC]8;;BEL" has both
// slots empty so the swap is a no-op.
//
// Upstream bug: github.com/charmbracelet/x/vt/osc.go:handleHyperlink
// (still present on main as of 2026-05-22). Fix submitted as
// https://github.com/charmbracelet/x/pull/868 — TODO: remove this
// workaround when that PR merges and we bump the vt dep. The
// round-trip test in vscreen_test.go will fail loudly if upstream
// lands but the workaround remains, since a fixed vt + our swap
// produces double-swapped output.
func fixOSC8Swap(in []byte) []byte {
	return osc8SwapRE.ReplaceAll(in, []byte("\x1b]8;$2;$1$3"))
}
