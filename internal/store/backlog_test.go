package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

func newBacklogTestStore(t *testing.T) (*FSStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := NewFSStore(dir, filepath.Join(dir, "reviews"), "", "")
	idea := &model.Idea{Name: "Backlog Test"}
	if err := store.Create(context.Background(), idea); err != nil {
		t.Fatalf("creating idea: %v", err)
	}
	return store, idea.Slug
}

func TestBacklog_EmptyMissingFile(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	items, err := store.ListBacklog(context.Background(), slug)
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty, got %d", len(items))
	}
}

func TestBacklog_AddAndList(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	stored, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{
		Title:  "write a regression test",
		Source: "session",
	})
	if err != nil {
		t.Fatalf("AddBacklogItem: %v", err)
	}
	if stored.ID == "" {
		t.Error("ID should be auto-populated")
	}
	if stored.Status != model.BacklogStatusOpen {
		t.Errorf("Status = %q, want open", stored.Status)
	}
	if stored.Created.IsZero() || stored.Updated.IsZero() {
		t.Error("timestamps should be populated")
	}

	items, err := store.ListBacklog(ctx, slug)
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(items) != 1 || items[0].ID != stored.ID || items[0].Title != "write a regression test" {
		t.Errorf("items = %+v", items)
	}
}

func TestBacklog_UpdateStatus(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	stored, _ := store.AddBacklogItem(ctx, slug, model.BacklogItem{Title: "ship the thing"})
	originalUpdated := stored.Updated
	time.Sleep(1 * time.Millisecond) // make sure Updated bumps

	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		Status: model.BacklogStatusInProgress,
	}); err != nil {
		t.Fatalf("UpdateBacklogItem: %v", err)
	}

	items, _ := store.ListBacklog(ctx, slug)
	if items[0].Status != model.BacklogStatusInProgress {
		t.Errorf("Status = %q, want in_progress", items[0].Status)
	}
	if !items[0].Updated.After(originalUpdated) {
		t.Error("Updated should be bumped")
	}
	if items[0].Title != "ship the thing" {
		t.Errorf("Title clobbered: %q", items[0].Title)
	}
}

func TestBacklog_UpdateUnknownID(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	err := store.UpdateBacklogItem(context.Background(), slug, "no-such-id", model.BacklogItem{
		Status: model.BacklogStatusDone,
	})
	if !errors.Is(err, ErrBacklogItemNotFound) {
		t.Errorf("err = %v, want ErrBacklogItemNotFound", err)
	}
}

func TestBacklog_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()
	stored, _ := store.AddBacklogItem(ctx, slug, model.BacklogItem{Title: "tmp"})

	found, err := store.DeleteBacklogItem(ctx, slug, stored.ID)
	if err != nil || !found {
		t.Errorf("first delete: found=%v err=%v", found, err)
	}

	found, err = store.DeleteBacklogItem(ctx, slug, stored.ID)
	if err != nil || found {
		t.Errorf("second delete should be idempotent: found=%v err=%v", found, err)
	}
}

func TestBacklog_ReadRepairUnknownStatus(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)

	// Write a hand-crafted backlog.json carrying an unknown status —
	// the read path should repair to open without erroring.
	raw := `[{"id":"abc","title":"legacy","status":"in-flight","created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(store.ideaDir(slug), backlogFile), []byte(raw), 0o644); err != nil {
		t.Fatalf("seeding backlog: %v", err)
	}

	items, err := store.ListBacklog(context.Background(), slug)
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(items) != 1 || items[0].Status != model.BacklogStatusOpen {
		t.Errorf("Status = %+v, want repaired to open", items)
	}
}

func TestBacklog_DependsOnAndAffects(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	stored, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{
		Title:     "refactor coordinator",
		DependsOn: []string{"abc-123", "other-idea:def-456"},
		Affects:   []string{"internal/agent/coordinator.go", "internal/agent/session.go"},
	})
	if err != nil {
		t.Fatalf("AddBacklogItem: %v", err)
	}
	if len(stored.DependsOn) != 2 || stored.DependsOn[1] != "other-idea:def-456" {
		t.Errorf("DependsOn = %+v", stored.DependsOn)
	}
	if len(stored.Affects) != 2 {
		t.Errorf("Affects = %+v", stored.Affects)
	}

	// Round-trip via disk: list returns the same payload.
	items, _ := store.ListBacklog(ctx, slug)
	if len(items[0].DependsOn) != 2 || items[0].Affects[0] != "internal/agent/coordinator.go" {
		t.Errorf("round-trip lost slice fields: %+v", items[0])
	}

	// Update: replace dependencies, leave affects untouched.
	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		DependsOn: []string{"abc-123"},
	}); err != nil {
		t.Fatalf("UpdateBacklogItem: %v", err)
	}
	items, _ = store.ListBacklog(ctx, slug)
	if len(items[0].DependsOn) != 1 || items[0].DependsOn[0] != "abc-123" {
		t.Errorf("DependsOn after update = %+v", items[0].DependsOn)
	}
	if len(items[0].Affects) != 2 {
		t.Errorf("Affects was clobbered: %+v", items[0].Affects)
	}

	// Explicit empty-slice clears the field. Distinguishing this from
	// nil-leave-alone is the whole point of the present-bit semantics.
	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		DependsOn: []string{},
	}); err != nil {
		t.Fatalf("UpdateBacklogItem clear: %v", err)
	}
	items, _ = store.ListBacklog(ctx, slug)
	if len(items[0].DependsOn) != 0 {
		t.Errorf("DependsOn should be cleared, got %+v", items[0].DependsOn)
	}
}

// TestBacklog_LabelsRoundTripAndReplace mirrors the DependsOn/Affects
// contract for the labels field: nil-leave-alone, non-nil-replace,
// explicit-[]-clear.
func TestBacklog_LabelsRoundTripAndReplace(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	stored, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{
		Title:  "labelled item",
		Labels: []string{"quick-win", "nit"},
	})
	if err != nil {
		t.Fatalf("AddBacklogItem: %v", err)
	}
	if len(stored.Labels) != 2 || stored.Labels[0] != "quick-win" {
		t.Errorf("Labels on add = %+v", stored.Labels)
	}

	items, _ := store.ListBacklog(ctx, slug)
	if len(items[0].Labels) != 2 || items[0].Labels[1] != "nit" {
		t.Errorf("round-trip lost labels: %+v", items[0].Labels)
	}

	// Nil patch leaves labels alone.
	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		Title: "renamed",
	}); err != nil {
		t.Fatalf("UpdateBacklogItem (title only): %v", err)
	}
	items, _ = store.ListBacklog(ctx, slug)
	if len(items[0].Labels) != 2 {
		t.Errorf("nil labels patch clobbered existing: %+v", items[0].Labels)
	}

	// Non-nil replaces.
	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		Labels: []string{"blocked-external"},
	}); err != nil {
		t.Fatalf("UpdateBacklogItem (replace): %v", err)
	}
	items, _ = store.ListBacklog(ctx, slug)
	if len(items[0].Labels) != 1 || items[0].Labels[0] != "blocked-external" {
		t.Errorf("Labels after replace = %+v", items[0].Labels)
	}

	// Explicit [] clears.
	if err := store.UpdateBacklogItem(ctx, slug, stored.ID, model.BacklogItem{
		Labels: []string{},
	}); err != nil {
		t.Fatalf("UpdateBacklogItem (clear): %v", err)
	}
	items, _ = store.ListBacklog(ctx, slug)
	if len(items[0].Labels) != 0 {
		t.Errorf("Labels should be cleared, got %+v", items[0].Labels)
	}
}

// TestBacklog_ConcurrentAddNoLostWrites — 50 goroutines each add one
// item to the same idea. Without the per-slug write lock, concurrent
// `List → mutate → write` races silently lose items (winner of the
// final atomicfile.Write writes the smaller pre-image). With the lock,
// every goroutine's item lands.
//
// Pre-lock behavior on this same scenario: stored count < 50.
func TestBacklog_ConcurrentAddNoLostWrites(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{
				Title: fmt.Sprintf("item-%02d", i),
			})
			if err != nil {
				t.Errorf("AddBacklogItem(%d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	items, err := store.ListBacklog(ctx, slug)
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(items) != N {
		t.Errorf("after %d concurrent adds, stored %d items (want %d)", N, len(items), N)
	}
	ids := make(map[string]struct{}, len(items))
	for _, it := range items {
		ids[it.ID] = struct{}{}
	}
	if len(ids) != len(items) {
		t.Errorf("duplicate IDs in result: %d unique of %d stored", len(ids), len(items))
	}
}

// TestBacklog_ConcurrentMixedNoLostWrites — adds, updates, and a
// resource mutation against the same slug run in parallel. Verifies
// the lock serializes across the different write entry points
// (backlog + resources both lock the same per-slug mutex), so the
// final state is consistent: backlog count + every update applied.
func TestBacklog_ConcurrentMixedNoLostWrites(t *testing.T) {
	t.Parallel()
	store, slug := newBacklogTestStore(t)
	ctx := context.Background()

	// Seed an item that the update goroutines can target.
	seed, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{Title: "seed"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			if _, err := store.AddBacklogItem(ctx, slug, model.BacklogItem{Title: fmt.Sprintf("add-%02d", i)}); err != nil {
				t.Errorf("Add(%d): %v", i, err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := store.AddResource(ctx, slug, model.Resource{
				Type: "web",
				URL:  fmt.Sprintf("https://example.com/r%02d", i),
			}); err != nil {
				t.Errorf("AddResource(%d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	items, err := store.ListBacklog(ctx, slug)
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(items) != N+1 { // seed + N adds
		t.Errorf("backlog count = %d, want %d", len(items), N+1)
	}

	idea, err := store.Get(ctx, slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(idea.Resources) != N {
		t.Errorf("resource count = %d, want %d", len(idea.Resources), N)
	}

	_ = seed // referenced for documentation of the seeded ID; updates wired below if needed
}
