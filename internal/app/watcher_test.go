package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// emitRecorder collects watcher emissions for assertions.
type emitRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
	wakeup chan struct{}
}

type recordedEvent struct {
	name string
	data any
}

func newEmitRecorder() *emitRecorder {
	return &emitRecorder{wakeup: make(chan struct{}, 64)}
}

func (r *emitRecorder) emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, recordedEvent{name: name, data: data})
	r.mu.Unlock()
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

// waitForSlug blocks until at least one EventIdeaChanged with the given
// slug has been emitted, or fails the test on timeout.
func (r *emitRecorder) waitForSlug(t *testing.T, slug string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		for _, ev := range r.events {
			if ev.name != EventIdeaChanged {
				continue
			}
			payload, ok := ev.data.(IdeaChangedPayload)
			if ok && payload.Slug == slug {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
		select {
		case <-r.wakeup:
		case <-deadline:
			r.mu.Lock()
			defer r.mu.Unlock()
			t.Fatalf("did not see %s for slug %q in %v; got %+v", EventIdeaChanged, slug, timeout, r.events)
		}
	}
}

// countSlug returns how many emits matched the slug.
func (r *emitRecorder) countSlug(slug string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.name != EventIdeaChanged {
			continue
		}
		payload, ok := ev.data.(IdeaChangedPayload)
		if ok && payload.Slug == slug {
			n++
		}
	}
	return n
}

func TestWatcher_EmitsOnFileWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rec := newEmitRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, nil); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "alpha", "idea.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rec.waitForSlug(t, "alpha", 2*time.Second)
}

func TestWatcher_DebouncesBurstedWrites(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rec := newEmitRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, nil); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// Five rapid writes inside the debounce window — should coalesce.
	target := filepath.Join(root, "beta", "context.md")
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(target, []byte("v"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	rec.waitForSlug(t, "beta", 2*time.Second)

	// Wait for an extra debounce window to confirm no follow-up emits.
	time.Sleep(2 * debounceWindow)
	if got := rec.countSlug("beta"); got != 1 {
		t.Errorf("expected 1 emit for %q, got %d", "beta", got)
	}
}

func TestWatcher_IgnoresDotfiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gamma"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rec := newEmitRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, nil); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// Editor noise — should not fire.
	for _, name := range []string{".idea.json.swp", ".DS_Store", ".tmp-12345"} {
		if err := os.WriteFile(filepath.Join(root, "gamma", name), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	time.Sleep(2 * debounceWindow)
	if got := rec.countSlug("gamma"); got != 0 {
		t.Errorf("expected 0 emits for dotfile-only changes, got %d", got)
	}
}

// summary.json is the headless summarizer's own sidecar; emitting
// idea:changed on its write would recursively re-trigger the
// summarizer's idea:changed debouncer. Watcher must filter these
// out at the source.
func TestWatcher_IgnoresSummaryJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gamma"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rec := newEmitRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, nil); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "gamma", "summary.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	time.Sleep(2 * debounceWindow)
	if got := rec.countSlug("gamma"); got != 0 {
		t.Errorf("summary.json triggered %d idea:changed emits, want 0", got)
	}
}

func TestWatcher_PicksUpNewIdeaDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	rec := newEmitRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, nil); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// Create a new idea dir, then a file inside it. Watcher should pick
	// up the dir on Create and start watching for inner-file events.
	ideaDir := filepath.Join(root, "delta")
	if err := os.Mkdir(ideaDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// First emit fires for the directory creation event itself.
	rec.waitForSlug(t, "delta", 2*time.Second)

	// Wait past the debounce so the next file write triggers a new emit.
	time.Sleep(2 * debounceWindow)

	// Now write a file inside the new dir.
	if err := os.WriteFile(filepath.Join(ideaDir, "idea.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for rec.countSlug("delta") < 2 {
		select {
		case <-rec.wakeup:
		case <-deadline:
			t.Fatalf("expected at least 2 emits after dir create + file write, got %d", rec.countSlug("delta"))
		}
	}
}

func TestWatcher_ReloadsConfigOnWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rec := newEmitRecorder()

	var (
		mu       sync.Mutex
		reloads  int
		reloadCh = make(chan struct{}, 8)
	)
	reload := func() error {
		mu.Lock()
		reloads++
		mu.Unlock()
		select {
		case reloadCh <- struct{}{}:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := startWatcher(ctx, root, rec.emit, reload); err != nil {
		t.Fatalf("startWatcher: %v", err)
	}

	// Write config.json at the root — should trigger reload, NOT an
	// idea:changed emit (config.json isn't an idea slug).
	if err := os.WriteFile(filepath.Join(root, configFilename), []byte(`{"branch_prefix":"pt/"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case <-reloadCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("reloadConfig was not called within 2s")
	}

	// No idea:changed emit should fire for config.json — it's not a slug.
	time.Sleep(2 * debounceWindow)
	if got := rec.countSlug(configFilename); got != 0 {
		t.Errorf("config.json triggered %d idea:changed emits, want 0", got)
	}
}
