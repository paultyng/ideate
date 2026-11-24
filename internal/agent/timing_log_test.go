package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type captureHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// captureSlog swaps the default logger; callers MUST NOT use t.Parallel.
func captureSlog(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// find returns attr maps of records with msg and a uuid attr equal to uuid.
func (h *captureHandler) find(msg, uuid string) []map[string]slog.Value {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]slog.Value
	for _, r := range h.recs {
		if r.Message != msg {
			continue
		}
		m := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value; return true })
		if v, ok := m["uuid"]; ok && v.String() == uuid {
			out = append(out, m)
		}
	}
	return out
}

func TestCoordinator_StartTimingLog(t *testing.T) {
	requirePTY(t)
	const msg = "coordinator: session start timing"
	h := captureSlog(t)
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		const id = "timing-ok"
		coord := NewCoordinator(t.TempDir())
		coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: buildTestAgent(t)})
		t.Cleanup(func() { _ = coord.Stop(ctx, id) })
		if _, err := coord.Start(ctx, SessionConfig{Name: "t", WorkingDir: t.TempDir(), AgentUUID: id, AgentType: "testagent", IdeaSlug: "my-slug"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		recs := h.find(msg, id)
		if len(recs) != 1 {
			t.Fatalf("timing lines = %d, want 1", len(recs))
		}
		m := recs[0]
		for _, k := range []string{"idea_slug", "agent_type", "run_ms", "total_ms"} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing key %q", k)
			}
		}
		if m["idea_slug"].String() != "my-slug" || m["agent_type"].String() != "testagent" {
			t.Errorf("attrs = %v", m)
		}
		if _, ok := m["err"]; ok {
			t.Error("unexpected err on success")
		}
	})

	t.Run("runner error", func(t *testing.T) {
		const id = "timing-err"
		coord := NewCoordinator(t.TempDir())
		coord.RegisterRunner("testagent", errorRunner{})
		if _, err := coord.Start(ctx, SessionConfig{Name: "t", WorkingDir: t.TempDir(), AgentUUID: id, AgentType: "testagent"}); err == nil {
			t.Fatal("want error")
		}
		recs := h.find(msg, id)
		if len(recs) != 1 {
			t.Fatalf("timing lines = %d, want 1", len(recs))
		}
		e, ok := recs[0]["err"]
		if !ok || !strings.Contains(e.String(), "boom") {
			t.Errorf("err attr = %v", e)
		}
	})

	t.Run("guard rejection", func(t *testing.T) {
		const id = "timing-reject"
		const rejected = "coordinator: session start rejected"
		coord := NewCoordinator(t.TempDir())
		gr := &gatingRunner{inner: &TestAgentRunner{BinaryPath: buildTestAgent(t)}, gate: make(chan struct{})}
		coord.RegisterRunner("testagent", gr)
		t.Cleanup(func() { _ = coord.Stop(ctx, id) })
		cfg := SessionConfig{Name: "t", WorkingDir: t.TempDir(), AgentUUID: id, AgentType: "testagent"}
		done := make(chan error, 1)
		go func() { _, err := coord.Start(ctx, cfg); done <- err }()
		deadline := time.Now().Add(10 * time.Second)
		for gr.calls.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("first Start never reached runner")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := coord.Start(ctx, cfg); !errors.Is(err, ErrSessionStarting) {
			t.Fatalf("err = %v, want ErrSessionStarting", err)
		}
		if n := len(h.find(rejected, id)); n != 1 {
			t.Errorf("rejected lines = %d, want 1", n)
		}
		if n := len(h.find(msg, id)); n != 0 {
			t.Errorf("timing lines while in flight = %d, want 0", n)
		}
		close(gr.gate)
		if err := <-done; err != nil {
			t.Fatalf("winner: %v", err)
		}
		if n := len(h.find(msg, id)); n != 1 {
			t.Errorf("timing lines after winner = %d, want 1", n)
		}
		if n := len(h.find(rejected, id)); n != 1 {
			t.Errorf("rejected lines = %d, want 1", n)
		}
	})
}

func TestSession_FirstOutputLog(t *testing.T) {
	requirePTY(t)
	h := captureSlog(t)
	ctx := context.Background()
	const id = "first-output"

	var mu sync.Mutex
	chunks := 0
	coord := NewCoordinator(t.TempDir())
	coord.RegisterRunner("testagent", &TestAgentRunner{BinaryPath: buildTestAgent(t)})
	coord.SetOutputHandler(func(_ string, _ []byte) { mu.Lock(); chunks++; mu.Unlock() })
	if _, err := coord.Start(ctx, SessionConfig{Name: "t", WorkingDir: t.TempDir(), AgentUUID: id, AgentType: "testagent"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = coord.Stop(ctx, id) })

	waitChunks := func(n int) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			c := chunks
			mu.Unlock()
			if c >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %d chunks (have %d)", n, c)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitChunks(1)
	mu.Lock()
	base := chunks
	mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := coord.Write(id, []byte("ping\r")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitChunks(base + 1)

	recs := h.find("session: first output", id)
	if len(recs) != 1 {
		t.Fatalf("first output lines = %d, want 1", len(recs))
	}
	if _, ok := recs[0]["since_spawn_ms"]; !ok {
		t.Error("missing since_spawn_ms")
	}
}
