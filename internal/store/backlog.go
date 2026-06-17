package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/paultyng/ideate/internal/atomicfile"
	"github.com/paultyng/ideate/internal/model"
)

// ErrBacklogItemNotFound is returned by UpdateBacklogItem when the
// requested item id is not present on the idea. DeleteBacklogItem
// uses the (found bool, err) convention instead — idempotent delete.
var ErrBacklogItemNotFound = errors.New("backlog item not found")

// backlogPath returns the absolute path to an idea's backlog.json.
func (s *FSStore) backlogPath(slug string) string {
	return filepath.Join(s.ideaDir(slug), backlogFile)
}

// ListBacklog returns every backlog item for the idea, sorted by
// Created ascending (oldest first — the order a human would expect
// reading a chronological todo list). Repairs unknown status values
// on read so callers don't have to defensively switch over.
//
// Missing backlog.json is the empty state: returns (nil, nil), not
// an error. Most ideas have no backlog yet.
func (s *FSStore) ListBacklog(_ context.Context, slug string) ([]model.BacklogItem, error) {
	data, err := os.ReadFile(s.backlogPath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading backlog file: %w", err)
	}
	var items []model.BacklogItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parsing backlog: %w", err)
	}
	for i := range items {
		model.RepairBacklogStatus(&items[i])
	}
	return items, nil
}

// AddBacklogItem appends item to the idea's backlog. ID, Created,
// Updated, and Status are populated server-side: callers pass in
// title/body/source and don't need to mint a UUID. Returns the
// stored item so the caller can surface the assigned ID.
func (s *FSStore) AddBacklogItem(ctx context.Context, slug string, item model.BacklogItem) (model.BacklogItem, error) {
	unlock := s.locks.Lock(slug)
	defer unlock()
	items, err := s.ListBacklog(ctx, slug)
	if err != nil {
		return model.BacklogItem{}, err
	}
	now := time.Now().UTC()
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Status == "" {
		item.Status = model.BacklogStatusOpen
	}
	model.RepairBacklogStatus(&item)
	if item.Created.IsZero() {
		item.Created = now
	}
	item.Updated = now
	items = append(items, item)
	if err := s.writeBacklog(slug, items); err != nil {
		return model.BacklogItem{}, err
	}
	return item, nil
}

// UpdateBacklogItem applies the non-empty fields of patch to the
// matching item. Empty string clears a string field (matches
// update_idea semantics); zero-value Status is treated as "leave
// alone" since the empty Status sentinel collides with field-not-
// supplied. Returns ErrBacklogItemNotFound when id is unknown.
func (s *FSStore) UpdateBacklogItem(ctx context.Context, slug, id string, patch model.BacklogItem) error {
	if id == "" {
		return fmt.Errorf("backlog item id is required")
	}
	unlock := s.locks.Lock(slug)
	defer unlock()
	items, err := s.ListBacklog(ctx, slug)
	if err != nil {
		return err
	}
	idx := -1
	for i := range items {
		if items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrBacklogItemNotFound
	}
	target := &items[idx]
	if patch.Title != "" {
		target.Title = patch.Title
	}
	// Body uses null-vs-empty-string semantics — but the in-process
	// patch shape can't distinguish "" from absent. Callers pass a
	// dedicated patch helper in the MCP layer that maps an explicit
	// "" to clear; for in-Go callers we keep the simpler rule
	// "non-empty patch wins, empty leaves alone".
	if patch.Body != "" {
		target.Body = patch.Body
	}
	if patch.Status != "" {
		model.RepairBacklogStatus(&patch)
		target.Status = patch.Status
	}
	if patch.Source != "" {
		target.Source = patch.Source
	}
	if patch.AssigneeSession != "" {
		target.AssigneeSession = patch.AssigneeSession
	}
	if patch.ExternalURL != "" {
		target.ExternalURL = patch.ExternalURL
	}
	// Slice fields use nil-vs-non-nil to distinguish "no change" from
	// "explicit replacement (possibly empty)". The MCP layer normalizes
	// "explicit []" into a non-nil zero-length slice before calling
	// store; in-Go callers do the same.
	if patch.DependsOn != nil {
		target.DependsOn = append([]string(nil), patch.DependsOn...)
	}
	if patch.Affects != nil {
		target.Affects = append([]string(nil), patch.Affects...)
	}
	target.Updated = time.Now().UTC()
	return s.writeBacklog(slug, items)
}

// DeleteBacklogItem removes the item with the matching ID. Returns
// (false, nil) when the id is unknown — idempotent like the
// resource delete path; callers that need a strict not-found
// signal can list first.
func (s *FSStore) DeleteBacklogItem(ctx context.Context, slug, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("backlog item id is required")
	}
	unlock := s.locks.Lock(slug)
	defer unlock()
	items, err := s.ListBacklog(ctx, slug)
	if err != nil {
		return false, err
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		items = append(items[:i], items[i+1:]...)
		if err := s.writeBacklog(slug, items); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// writeBacklog persists the items array atomically. Empty arrays
// still write `[]` to disk so a list call after a complete delete
// sees the empty state rather than the missing-file fallback —
// either is correct, but keeping the file present makes the
// "backlog has been touched" signal easier to read off disk.
func (s *FSStore) writeBacklog(slug string, items []model.BacklogItem) error {
	dir := s.ideaDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating idea dir: %w", err)
	}
	if items == nil {
		items = []model.BacklogItem{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling backlog: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write(s.backlogPath(slug), data, 0o644); err != nil {
		return fmt.Errorf("writing backlog file: %w", err)
	}
	slog.Debug("wrote backlog",
		slog.String("slug", slug),
		slog.Int("count", len(items)),
	)
	return nil
}
