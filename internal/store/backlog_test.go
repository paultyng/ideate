package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
