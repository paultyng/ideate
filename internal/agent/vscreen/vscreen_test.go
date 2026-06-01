package vscreen

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSnapshotRendersFedText(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("hello world\r\n"))

	snap := b.Snapshot()
	if !bytes.Contains(snap, []byte("hello world")) {
		t.Fatalf("snapshot missing fed text: %q", snap)
	}
}

func TestSnapshotSurvivesByteRingWraparound(t *testing.T) {
	t.Parallel()
	// The whole point of A: state is bounded by rows×cols, so the most
	// recent visible content is always present regardless of how many
	// bytes have streamed through.
	b := New(80, 24)

	// Stream ~1 MB of filler then a marker. Old approach (4 MB ring) would
	// preserve this because it fits; the test still proves the marker is
	// the *trailing* visible content.
	for range 1000 {
		b.Feed([]byte(strings.Repeat("filler ", 100) + "\r\n"))
	}
	b.Feed([]byte("UNIQUE_MARKER_LINE\r\n"))

	snap := b.Snapshot()
	if !bytes.Contains(snap, []byte("UNIQUE_MARKER_LINE")) {
		t.Fatal("snapshot missing trailing marker")
	}
}

func TestSnapshotPreservesSGR(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	// Red foreground "hi" then reset.
	b.Feed([]byte("\x1b[31mhi\x1b[0m"))

	snap := b.Snapshot()
	if !bytes.Contains(snap, []byte("hi")) {
		t.Fatalf("snapshot missing text: %q", snap)
	}
	if !bytes.Contains(snap, []byte("\x1b[")) {
		t.Errorf("snapshot lacks SGR escape — Render() should re-emit styling: %q", snap)
	}
}

func TestResizeDoesNotPanic(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("before resize\r\n"))
	b.Resize(120, 40)
	b.Feed([]byte("after resize\r\n"))

	snap := b.Snapshot()
	if !bytes.Contains(snap, []byte("after resize")) {
		t.Fatalf("snapshot missing post-resize content: %q", snap)
	}
}

// Shrink-with-content: a line shorter than the new width should still
// be readable at column 0 after the resize, no garbage characters in
// previously-visible cells beyond the new bounds.
func TestResize_ShrinkPreservesShortLines(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("HELLO\r\n"))
	b.Resize(40, 24)

	cols, _ := b.Dimensions()
	if cols != 40 {
		t.Fatalf("post-resize cols = %d, want 40", cols)
	}
	got := b.CellContentAt(0, 0) + b.CellContentAt(1, 0) + b.CellContentAt(2, 0) +
		b.CellContentAt(3, 0) + b.CellContentAt(4, 0)
	if got != "HELLO" {
		t.Errorf("row 0 cols 0-4 = %q, want %q", got, "HELLO")
	}
}

// Grow doesn't synthesize content where there wasn't any. Cells beyond
// the prior width should be empty after a grow.
func TestResize_GrowLeavesNewCellsBlank(t *testing.T) {
	t.Parallel()
	b := New(40, 24)
	b.Feed([]byte("XYZ\r\n"))
	b.Resize(80, 24)

	cols, _ := b.Dimensions()
	if cols != 80 {
		t.Fatalf("post-resize cols = %d, want 80", cols)
	}
	if got := b.CellContentAt(50, 0); got != "" && got != " " {
		t.Errorf("expected blank cell at col 50 after grow, got %q", got)
	}
}

// Snapshot after a resize must still emit CRLF line separators (not
// bare LF), otherwise xterm.js re-mounts staircase. This is the same
// invariant TestSnapshotUsesCRLFBetweenLines covers pre-resize, but
// resize touches the emulator's internal layout and could regress
// the line-ending behavior independently.
func TestResize_SnapshotKeepsCRLFInvariant(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("a\r\nb\r\nc\r\n"))
	b.Resize(120, 40)

	snap := b.Snapshot()
	if bytes.Contains(snap, []byte("\r\n")) {
		return // good — at least some CRLFs present
	}
	if bytes.Contains(snap, []byte("\n")) {
		t.Fatalf("post-resize snapshot uses bare LF — would staircase. snap=%q", snap)
	}
}

// Scrollback survives a resize. Lines fed pre-resize and pushed into
// scrollback by subsequent output should still appear in the post-
// resize snapshot — otherwise re-mount after a window resize would
// lose the user's history.
func TestResize_ScrollbackSurvives(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	// Push 50 lines so the early ones definitely live in scrollback.
	for i := range 50 {
		b.Feed([]byte("hist " + strconv.Itoa(i) + "\r\n"))
	}
	b.Resize(60, 24)

	snap := b.Snapshot()
	// "hist 0" rolled into scrollback (24-row screen + 26 lines past it).
	// It must still appear after the resize.
	if !bytes.Contains(snap, []byte("hist 0\r\n")) {
		t.Errorf("scrollback line 'hist 0' missing after resize — snapshot lost history")
	}
}

// drainLoop and binding handler will hit the buffer concurrently. Make
// sure Feed/Snapshot don't race or corrupt state under the race detector.
func TestConcurrentFeedAndSnapshot(t *testing.T) {
	t.Parallel()
	b := New(80, 24)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 1000 {
			b.Feed([]byte("line\r\n"))
		}
	}()

	go func() {
		defer wg.Done()
		for range 1000 {
			_ = b.Snapshot()
		}
	}()

	wg.Wait()
}

func TestZeroDimsDefaults(t *testing.T) {
	t.Parallel()
	b := New(0, 0)
	b.Feed([]byte("ok"))
	snap := b.Snapshot()
	if len(snap) == 0 {
		t.Fatal("expected non-empty snapshot at default dims")
	}
}

// Snapshots replay into xterm.js, which (in default LNM-off mode) treats
// LF as cursor-down-only — so a snapshot with bare \n separators causes
// each subsequent line to start at the previous line's end column,
// producing a staircased "wrapping offset" pattern when the user
// switches between sessions. Snapshots must use CRLF so each line starts
// at column 0.
func TestSnapshotUsesCRLFBetweenLines(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("line one\r\nline two\r\nline three\r\n"))

	snap := b.Snapshot()
	if bytes.Contains(snap, []byte("\r\n")) {
		return // good
	}
	if bytes.Contains(snap, []byte("\n")) {
		t.Fatalf("snapshot uses bare LF — xterm.js will staircase lines. snap=%q", snap)
	}
}

// Bumping the scrollback gives users meaningful backscroll after a
// session toggle — pre-bump (1000 lines) the most recent screenful
// was all that survived a re-mount via Snapshot. Asserts that lines
// well beyond the prior xterm-default budget are still reachable.
func TestScrollback_RetainsBeyondLegacyDefault(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	for i := range 5000 {
		// Emit "line N\r\n" so scanning the snapshot for an exact marker
		// is unambiguous (no shared substrings between adjacent lines).
		b.Feed([]byte("line " + strconv.Itoa(i) + "\r\n"))
	}
	snap := b.Snapshot()
	if !bytes.Contains(snap, []byte("line 100\r\n")) {
		t.Errorf("expected early line 'line 100' to survive in scrollback; not found")
	}
	if !bytes.Contains(snap, []byte("line 4999")) {
		t.Errorf("expected most recent line 'line 4999' to be present")
	}
}

// IDEATE_VSCREEN_SCROLLBACK lets memory-constrained users dial down
// the scrollback. Asserts the env override flows through New().
func TestScrollback_EnvOverride_ClampsToBounds(t *testing.T) {
	cases := []struct {
		raw      string
		retained string // must be present
		evicted  string // must NOT be present (older than scrollback)
	}{
		// Tight cap: ~120 lines (bounded above min). The first line should
		// be evicted; the last line is always present.
		{"120", "line 1199\r\n", "line 0\r\n"},
		// Below min: clamps to 100. First line evicted.
		{"5", "line 1199\r\n", "line 0\r\n"},
		// Bad value falls back to the default — first line still present.
		{"not-a-number", "line 0\r\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(scrollbackEnvVar, tc.raw)
			b := New(80, 24)
			for i := range 1200 {
				b.Feed([]byte("line " + strconv.Itoa(i) + "\r\n"))
			}
			snap := b.Snapshot()
			if !bytes.Contains(snap, []byte(tc.retained)) {
				t.Errorf("expected retained marker %q in snapshot", tc.retained)
			}
			if tc.evicted != "" && bytes.Contains(snap, []byte(tc.evicted)) {
				t.Errorf("expected evicted marker %q to be gone", tc.evicted)
			}
		})
	}
}

// Regression: vt.Emulator's parser dispatches response bytes (DA1, cursor
// position reports, etc.) into an internal unbuffered io.Pipe. Without a
// drain goroutine, the first such Feed deadlocks the entire drain loop
// and the frontend never sees output. claude TUI emits these on startup,
// so the bug looked like "no terminal output at all" once a real agent
// connected — even though no synthetic SGR test caught it.
func TestFeedDoesNotDeadlockOnQueryEscapes(t *testing.T) {
	t.Parallel()
	b := New(80, 24)

	done := make(chan struct{})
	go func() {
		// ESC[c — DA1 device-attributes query. Triggers a response.
		b.Feed([]byte("\x1b[c"))
		// ESC[6n — cursor-position report query. Triggers a response.
		b.Feed([]byte("\x1b[6n"))
		// Plain text afterward to make sure later Feeds also return.
		b.Feed([]byte("after"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Feed deadlocked on emulator response writes — drain goroutine missing")
	}

	if !bytes.Contains(b.Snapshot(), []byte("after")) {
		t.Fatalf("snapshot missing post-query content")
	}
}

// Alt-screen Snapshot must (a) emit the DECSET ?1049 enter sequence
// so a fresh xterm switches to its alternate buffer before receiving
// the rendered TUI chrome, and (b) omit the main-screen scrollback
// prepend so pre-?1049h history doesn't bleed into the alt buffer.
// Without (a), Claude's bottom-input row ends up in xterm's main-
// buffer scrollback on subsequent live output (the ghosting bug).
func TestBuffer_Snapshot_AltScreen_EmitsEnterSequenceAndOmitsScrollback(t *testing.T) {
	t.Parallel()
	b := New(80, 24)

	// Seed the main-screen scrollback with a sentinel that must NOT
	// appear in the alt-screen snapshot. Push enough lines to scroll
	// the marker off the visible region so it lives in scrollback.
	b.Feed([]byte("MAIN-SENTINEL\r\n"))
	for range 30 {
		b.Feed([]byte("filler\r\n"))
	}

	// Enter alt-screen and draw a TUI body.
	b.Feed([]byte("\x1b[?1049h"))
	b.Feed([]byte("TUI-BODY"))

	snap := b.Snapshot()

	// (a) Enter sequence at the start.
	if !bytes.HasPrefix(snap, []byte("\x1b[?1049h")) {
		head := snap
		if len(head) > 40 {
			head = head[:40]
		}
		t.Errorf("snapshot does not start with \\x1b[?1049h: %q", head)
	}
	// Alt-screen body is present.
	if !bytes.Contains(snap, []byte("TUI-BODY")) {
		t.Errorf("snapshot missing alt-screen body: %q", snap)
	}
	// (b) Main-screen scrollback omitted — sentinel must not appear.
	if bytes.Contains(snap, []byte("MAIN-SENTINEL")) {
		t.Errorf("alt-screen snapshot leaked main-screen scrollback sentinel: %q", snap)
	}
	// "filler" rows (also pure main-screen content) likewise omitted.
	if bytes.Contains(snap, []byte("filler")) {
		t.Errorf("alt-screen snapshot leaked main-screen filler: %q", snap)
	}
}

// Main-screen Snapshot keeps its existing scrollback + visible
// behavior. Guards against accidentally short-circuiting the
// non-alt-screen path.
func TestBuffer_Snapshot_MainScreen_UnchangedBehavior(t *testing.T) {
	t.Parallel()
	b := New(80, 24)

	// Seed scrollback by pushing more rows than the visible 24.
	b.Feed([]byte("SCROLLED-OFF\r\n"))
	for i := range 30 {
		b.Feed([]byte("row " + strconv.Itoa(i) + "\r\n"))
	}

	snap := b.Snapshot()

	if bytes.HasPrefix(snap, []byte("\x1b[?1049h")) {
		head := snap
		if len(head) > 40 {
			head = head[:40]
		}
		t.Errorf("main-screen snapshot must not emit alt-screen enter: %q", head)
	}
	if !bytes.Contains(snap, []byte("SCROLLED-OFF")) {
		t.Errorf("main-screen snapshot dropped scrollback content: %q", snap)
	}
}

// Alt-screen Snapshot must end with a CSI CUP escape that points at
// vt's current cursor coords, so xterm parks its cursor in the same
// row vt thinks the cursor lives in (typically Claude's input row).
// Without this, xterm's cursor sits at end-of-last-rendered-character
// and the user's keystrokes echo in the wrong row until the agent's
// next full repaint.
func TestBuffer_Snapshot_AltScreen_EndsWithCursorPositionEscape(t *testing.T) {
	t.Parallel()
	b := New(80, 24)

	// Enter alt-screen, then move the cursor to a known position
	// via CSI CUP. row=5, col=7 (1-indexed in CSI, 0-indexed in vt).
	b.Feed([]byte("\x1b[?1049h"))
	b.Feed([]byte("\x1b[5;7H"))
	b.Feed([]byte("X"))

	snap := b.Snapshot()

	// The cursor escape must appear AFTER the rendered body, not
	// somewhere in the middle. Check the tail.
	const wantTail = "\x1b[5;8H" // CUP after writing 'X' at (5,7) lands at (5,8)
	if !bytes.HasSuffix(snap, []byte(wantTail)) {
		// Show a useful tail-suffix for debugging.
		tail := snap
		if len(tail) > 30 {
			tail = tail[len(tail)-30:]
		}
		t.Errorf("snapshot does not end with cursor escape %q; tail=%q", wantTail, tail)
	}
}

// Main-screen Snapshot must NOT emit a trailing cursor escape — the
// guard against accidentally cross-branching the new behavior into
// the main-screen path, where shell-style sessions naturally end at
// the prompt and don't need cursor restoration.
func TestBuffer_Snapshot_MainScreen_NoCursorEscapeAppended(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	b.Feed([]byte("hello"))

	snap := b.Snapshot()

	// Look at the last 20 bytes for any CSI CUP shape (`\x1b[<n>;<n>H`).
	tail := snap
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	if bytes.Contains(tail, []byte("H")) && bytes.Contains(tail, []byte("\x1b[")) {
		// More careful check: any "\x1b[" followed by digits and ending
		// at "H" is a CUP.
		idx := bytes.LastIndex(tail, []byte("\x1b["))
		if idx >= 0 && bytes.IndexByte(tail[idx:], 'H') > 0 {
			t.Errorf("main-screen snapshot appears to emit a trailing cursor escape; tail=%q", tail)
		}
	}
}

// OSC 8 hyperlinks must round-trip through Feed+Snapshot with URL and
// params in the correct slots. charmbracelet/x/vt's handleHyperlink
// stores parts[1] (the params slot) into Link.URL and parts[2] (the
// URI slot) into Link.Params; without our fixOSC8Swap workaround in
// Snapshot, the bytes come out with the two slots swapped — xterm
// would then see a link with an empty URI and render styled-but-
// unclickable text. See the fixOSC8Swap comment in vscreen.go.
//
// If this test fails after a vt dep bump with "want zero-params form
// got non-empty params slot", the upstream fix (charmbracelet/x#868)
// has landed and our workaround now produces double-swapped output.
// Delete fixOSC8Swap (and this test's expectation; replace with a
// simpler "URI in URI slot" check that passes either way).
func TestBuffer_Snapshot_OSC8_RoundtripsWithCorrectSlots(t *testing.T) {
	t.Parallel()
	b := New(80, 24)
	// Common form emitted by `gh`, `claude`, coreutils, and testagent's
	// `/link` command: empty params, URI populated.
	in := []byte("\x1b]8;;https://example.com/foo?bar=1\x1b\\Link Text\x1b]8;;\x1b\\\r\n")
	b.Feed(in)

	snap := b.Snapshot()

	// The opener must have an empty params slot and the URI in the URI
	// slot — i.e., ESC ] 8 ; ; <uri> ST. Any output of the form
	// ESC ] 8 ; <uri> ; ST (URI in the params slot) is the swap bug
	// reaching our consumers.
	wantOpener := []byte("\x1b]8;;https://example.com/foo?bar=1")
	if !bytes.Contains(snap, wantOpener) {
		t.Errorf("OSC 8 opener missing or in wrong slot.\n  want substring: %q\n  got:            %q", wantOpener, snap)
	}
	// And we must NOT see the swapped form.
	dontWant := []byte("\x1b]8;https://example.com/foo?bar=1;")
	if bytes.Contains(snap, dontWant) {
		t.Errorf("OSC 8 opener emitted with URI/params slots swapped.\n  must not contain: %q\n  got:              %q", dontWant, snap)
	}
}

// TestBuffer_Snapshot_PersistOnStop: write some chunks, close the buffer,
// assert the snapshot file exists and ReadSnapshot returns bytes matching
// Buffer.Snapshot(). On-disk bytes are raw (no fixOSC8Swap); ReadSnapshot
// applies the swap on read, matching Snapshot()'s output.
func TestBuffer_Snapshot_PersistOnStop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "sessions", "abc.snapshot.ans")

	b := New(80, 24)
	b.SetSnapshotPath(snapPath)
	b.Feed([]byte("persist-on-stop marker\r\n"))

	want := b.Snapshot()
	b.Close()

	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("snapshot file not written on Close: %v", err)
	}
	// ReadSnapshot applies fixOSC8Swap on read; Snapshot() also applies it.
	// Both paths must produce the same bytes.
	got, err := ReadSnapshot(snapPath)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("snapshot file is empty after Close")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadSnapshot differs from Buffer.Snapshot()\n  disk: %q\n  snap: %q", got, want)
	}
}

// TestBuffer_Snapshot_PeriodicPersist_ByChunkCount: feed 500 chunks,
// assert the snapshot file is written due to the chunk threshold.
func TestBuffer_Snapshot_PeriodicPersist_ByChunkCount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "sessions", "abc.snapshot.ans")

	b := New(80, 24)
	b.SetSnapshotPath(snapPath)

	for i := range snapshotChunkThreshold - 1 {
		b.Feed([]byte("x" + strconv.Itoa(i) + "\r\n"))
	}
	// File should NOT be written yet (499 chunks < 500 threshold).
	if _, err := os.Stat(snapPath); err == nil {
		t.Error("snapshot written before threshold reached")
	}

	// 500th chunk fires the write.
	b.Feed([]byte("trigger\r\n"))

	got, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot not written after %d chunks: %v", snapshotChunkThreshold, err)
	}
	if len(got) == 0 {
		t.Fatal("snapshot file is empty after chunk threshold")
	}
	if !bytes.Contains(got, []byte("trigger")) {
		t.Errorf("snapshot missing content fed before write: %q", got)
	}
}

// TestBuffer_Snapshot_PeriodicPersist_ByTime: feed a few chunks, advance
// a fake clock past 31s via the buffer's clock injection, feed one more
// chunk, assert the file is written due to the time threshold.
func TestBuffer_Snapshot_PeriodicPersist_ByTime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "sessions", "abc.snapshot.ans")

	b := New(80, 24)
	// Replace the clock with a manually-advancing fake. Start at T0.
	fakeNow := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b.clock = func() time.Time { return fakeNow }
	b.SetSnapshotPath(snapPath)

	// Feed a few chunks — well below the count threshold.
	for range 5 {
		b.Feed([]byte("early\r\n"))
	}
	// No file yet: count is far below 500 and fake time has not advanced.
	if _, err := os.Stat(snapPath); err == nil {
		t.Error("snapshot written before time threshold")
	}

	// Advance fake clock past the 30 s threshold.
	fakeNow = fakeNow.Add(snapshotTimeThreshold + time.Second)

	// One more Feed to trigger the check.
	b.Feed([]byte("after-delay\r\n"))

	got, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot not written after time threshold: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("snapshot file is empty after time threshold")
	}
}

// TestReadSnapshot applies fixOSC8Swap and returns (nil, nil) for a
// missing file.
func TestReadSnapshot(t *testing.T) {
	t.Parallel()

	// Missing file → nil, nil.
	got, err := ReadSnapshot(filepath.Join(t.TempDir(), "no-such.snapshot.ans"))
	if err != nil || got != nil {
		t.Errorf("missing file: got (%q, %v), want (nil, nil)", got, err)
	}

	// Written file round-trips.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.snapshot.ans")
	b := New(80, 24)
	b.Feed([]byte("hello ReadSnapshot\r\n"))
	want := b.Snapshot()
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err = ReadSnapshot(path)
	if err != nil {
		t.Fatalf("ReadSnapshot err: %v", err)
	}
	// fixOSC8Swap is idempotent on content that has no OSC8 links.
	if !bytes.Equal(got, want) {
		t.Errorf("ReadSnapshot = %q, want %q", got, want)
	}
}
