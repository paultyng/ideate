package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/store"
)

// deleteResourceCall records a single call to fakeStore.DeleteResource.
type deleteResourceCall struct {
	slug    string
	url     string
	deleted bool
	err     error
}

// fakeStore implements IdeaStore for testing.
type fakeStore struct {
	ideas   map[string]*model.Idea
	history []model.HistoryEvent
	// repos maps slug → set of allowed absolute repo paths. A nil/empty set
	// means ResolveRepoPath returns the input unchanged (preserves existing
	// test behavior that wasn't exercising path validation).
	repos map[string]map[string]bool

	// sessions maps slug → AgentSession records, used by the orchestration
	// tools (`list_sessions`, `get_session`).
	sessions map[string][]model.AgentSession

	// backlog maps slug → ordered items. Mirrors the real store's
	// oldest-first append behavior.
	backlog map[string][]model.BacklogItem

	// In-memory review storage so the fakeStore satisfies the MCP IdeaStore
	// interface without touching the filesystem. Tests that need cross-call
	// review state share the map; CancelReview/Submit mutate in place.
	reviewsMu sync.Mutex
	reviews   map[string]*review.Review

	// Lifecycle error injection for tests.
	archiveErr   error
	unarchiveErr error
	pauseErr     error
	resumeErr    error

	// deleteResourceResult controls the return value of DeleteResource.
	deleteResourceResult deleteResourceCall
	// deleteResourceCalls records all calls to DeleteResource.
	deleteResourceCalls []deleteResourceCall
}

func (s *fakeStore) MatchResourceURLs(_ context.Context, urls []string) (map[string][]store.ResourceMatch, error) {
	out := make(map[string][]store.ResourceMatch, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	keys := make(map[string][]string, len(urls))
	for _, raw := range urls {
		out[raw] = []store.ResourceMatch{}
		key := model.NormalizeResourceKey(model.Resource{URL: raw})
		if key == "" {
			continue
		}
		keys[key] = append(keys[key], raw)
	}
	for _, idea := range s.ideas {
		for _, res := range idea.Resources {
			key := model.NormalizeResourceKey(res)
			origs, ok := keys[key]
			if !ok {
				continue
			}
			match := store.ResourceMatch{
				Slug:                 idea.Slug,
				Name:                 idea.Name,
				ResourceType:         res.Type,
				ResourceLabel:        res.Label,
				IdeaURL:              model.IdeaURL(idea.Slug),
				IdeaActiveSessionURL: model.IdeaActiveSessionURL(idea.Slug),
			}
			for _, orig := range origs {
				out[orig] = append(out[orig], match)
			}
		}
	}
	return out, nil
}

func (s *fakeStore) ListBacklog(_ context.Context, slug string) ([]model.BacklogItem, error) {
	if s.backlog == nil {
		return nil, nil
	}
	return append([]model.BacklogItem{}, s.backlog[slug]...), nil
}

func (s *fakeStore) AddBacklogItem(_ context.Context, slug string, item model.BacklogItem) (model.BacklogItem, error) {
	if s.backlog == nil {
		s.backlog = map[string][]model.BacklogItem{}
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("fake-%d", len(s.backlog[slug])+1)
	}
	if item.Status == "" {
		item.Status = model.BacklogStatusOpen
	}
	model.RepairBacklogStatus(&item)
	now := time.Now().UTC()
	if item.Created.IsZero() {
		item.Created = now
	}
	item.Updated = now
	s.backlog[slug] = append(s.backlog[slug], item)
	return item, nil
}

func (s *fakeStore) UpdateBacklogItem(_ context.Context, slug, id string, patch model.BacklogItem) error {
	items := s.backlog[slug]
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if patch.Title != "" {
			items[i].Title = patch.Title
		}
		if patch.Body != "" {
			items[i].Body = patch.Body
		}
		if patch.Status != "" {
			model.RepairBacklogStatus(&patch)
			items[i].Status = patch.Status
		}
		if patch.DependsOn != nil {
			items[i].DependsOn = append([]string(nil), patch.DependsOn...)
		}
		if patch.Affects != nil {
			items[i].Affects = append([]string(nil), patch.Affects...)
		}
		items[i].Updated = time.Now().UTC()
		s.backlog[slug] = items
		return nil
	}
	return store.ErrBacklogItemNotFound
}

func (s *fakeStore) DeleteBacklogItem(_ context.Context, slug, id string) (bool, error) {
	items := s.backlog[slug]
	for i := range items {
		if items[i].ID != id {
			continue
		}
		s.backlog[slug] = append(items[:i], items[i+1:]...)
		return true, nil
	}
	return false, nil
}

func (s *fakeStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	if s.sessions == nil {
		return nil, nil
	}
	return s.sessions[slug], nil
}

// RenameIdea is unused by the existing fakeStore-backed tests; the
// rename_idea handler has its own test that runs against the real
// FSStore. Keep this stub minimal so we don't drift on semantics.
func (s *fakeStore) RenameIdea(_ context.Context, _, _ string) (*store.RenameResult, error) {
	return nil, fmt.Errorf("fakeStore.RenameIdea: not implemented")
}

// Delete is unused by fakeStore-backed tests for the same reason as
// RenameIdea — the dedicated handler test exercises the real store.
func (s *fakeStore) Delete(_ context.Context, _ string, _ bool) error {
	return fmt.Errorf("fakeStore.Delete: not implemented")
}

func (s *fakeStore) Archive(_ context.Context, _ string, _ bool) (*model.ArchiveReport, error) {
	if s.archiveErr != nil {
		return nil, s.archiveErr
	}
	return &model.ArchiveReport{}, nil
}

func (s *fakeStore) Unarchive(_ context.Context, _ string) (*model.UnarchiveReport, error) {
	if s.unarchiveErr != nil {
		return nil, s.unarchiveErr
	}
	return &model.UnarchiveReport{}, nil
}

func (s *fakeStore) Pause(_ context.Context, _ string, _ *time.Time) error {
	return s.pauseErr
}

func (s *fakeStore) Resume(_ context.Context, _ string) error {
	return s.resumeErr
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		ideas:   make(map[string]*model.Idea),
		reviews: make(map[string]*review.Review),
	}
}

func (s *fakeStore) Get(_ context.Context, slug string) (*model.Idea, error) {
	idea, ok := s.ideas[slug]
	if !ok {
		return nil, fmt.Errorf("idea %q not found", slug)
	}
	// Return a copy so mutations don't bypass Update.
	cp := *idea
	cp.Resources = append([]model.Resource(nil), idea.Resources...)
	return &cp, nil
}

func (s *fakeStore) Update(_ context.Context, idea *model.Idea) error {
	s.ideas[idea.Slug] = idea
	return nil
}

func (s *fakeStore) AddResource(_ context.Context, slug string, res model.Resource) error {
	idea, ok := s.ideas[slug]
	if !ok {
		return fmt.Errorf("idea %q not found", slug)
	}
	model.UpsertResource(idea, res)
	return nil
}

func (s *fakeStore) DeleteResource(_ context.Context, slug, url string) (bool, error) {
	call := deleteResourceCall{slug: slug, url: url, deleted: s.deleteResourceResult.deleted, err: s.deleteResourceResult.err}
	s.deleteResourceCalls = append(s.deleteResourceCalls, call)
	return call.deleted, call.err
}

func (s *fakeStore) AppendHistory(_ context.Context, _ string, event model.HistoryEvent) error {
	s.history = append(s.history, event)
	return nil
}

func (s *fakeStore) ResolveRepoPath(_ context.Context, slug, repoPath string) (string, error) {
	allowed := s.repos[slug]
	if len(allowed) == 0 {
		return repoPath, nil
	}
	if !allowed[repoPath] {
		return "", fmt.Errorf("repo path %q is not configured for idea %q", repoPath, slug)
	}
	return repoPath, nil
}

func (s *fakeStore) LinkRepo(_ context.Context, _, _, _, _ string) (string, error) {
	return "", fmt.Errorf("LinkRepo not implemented in fakeStore")
}

func (s *fakeStore) UnlinkRepo(_ context.Context, _, _ string, _ bool) error {
	return fmt.Errorf("UnlinkRepo not implemented in fakeStore")
}

func (s *fakeStore) ListRepos(_ context.Context, _ string) ([]store.RepoLink, error) {
	return nil, nil
}

func (s *fakeStore) List(_ context.Context) ([]model.Idea, error) {
	out := make([]model.Idea, 0, len(s.ideas))
	for _, idea := range s.ideas {
		out = append(out, *idea)
	}
	return out, nil
}

func (s *fakeStore) Create(_ context.Context, idea *model.Idea) error {
	if idea.Slug == "" {
		idea.Slug = "test-" + idea.Name
	}
	s.ideas[idea.Slug] = idea
	return nil
}

func (s *fakeStore) CreateOrReopenDiffReview(opts review.CreateOpts) (*review.Review, bool, error) {
	id := review.GenerateReviewID(opts.BaseCommit, opts.HeadCommit, opts.HeadRef)
	s.reviewsMu.Lock()
	defer s.reviewsMu.Unlock()

	if existing, ok := s.reviews[id]; ok {
		if existing.Status == review.ReviewPending {
			return nil, false, fmt.Errorf("%w: %s", store.ErrReviewInProgress, id)
		}
		if existing.Status == review.ReviewCancelled {
			existing.Status = review.ReviewPending
			existing.Completed = nil
			existing.Body = ""
			existing.Event = ""
			existing.Comments = nil
			if opts.SessionID != "" {
				existing.Session = opts.SessionID
			}
			return existing, true, nil
		}
	}
	r := &review.Review{
		ID:         id,
		Kind:       review.KindDiff,
		Status:     review.ReviewPending,
		Created:    time.Now(),
		Session:    opts.SessionID,
		IdeaSlug:   opts.IdeaSlug,
		Repo:       opts.Repo,
		BaseCommit: opts.BaseCommit,
		HeadCommit: opts.HeadCommit,
		HeadRef:    opts.HeadRef,
	}
	s.reviews[id] = r
	return r, false, nil
}

func (s *fakeStore) CreateOrReopenMarkdownReview(opts review.MarkdownCreateOpts) (*review.Review, bool, error) {
	id := review.GenerateMarkdownReviewID(opts.Path)
	s.reviewsMu.Lock()
	defer s.reviewsMu.Unlock()

	if existing, ok := s.reviews[id]; ok {
		if existing.Status == review.ReviewPending {
			return nil, false, fmt.Errorf("%w: %s", store.ErrReviewInProgress, id)
		}
		if existing.Status == review.ReviewCancelled {
			existing.Status = review.ReviewPending
			existing.Completed = nil
			existing.Body = ""
			existing.Event = ""
			existing.Markdown = &review.MarkdownPayload{
				Path:     opts.Path,
				Original: opts.Original,
			}
			if opts.SessionID != "" {
				existing.Session = opts.SessionID
			}
			if opts.IdeaSlug != "" {
				existing.IdeaSlug = opts.IdeaSlug
			}
			return existing, true, nil
		}
	}
	r := &review.Review{
		ID:       id,
		Kind:     review.KindMarkdown,
		Status:   review.ReviewPending,
		Created:  time.Now(),
		Session:  opts.SessionID,
		IdeaSlug: opts.IdeaSlug,
		Markdown: &review.MarkdownPayload{
			Path:     opts.Path,
			Original: opts.Original,
		},
	}
	s.reviews[id] = r
	return r, false, nil
}

func (s *fakeStore) CancelReview(id string) (*review.Review, error) {
	if err := review.ValidID(id); err != nil {
		return nil, err
	}
	s.reviewsMu.Lock()
	defer s.reviewsMu.Unlock()
	r, ok := s.reviews[id]
	if !ok {
		return nil, fmt.Errorf("review %q not found", id)
	}
	if r.Status != review.ReviewPending {
		return nil, fmt.Errorf("review %q is %s, not pending", id, r.Status)
	}
	now := time.Now()
	r.Status = review.ReviewCancelled
	r.Completed = &now
	cp := *r
	return &cp, nil
}

func (s *fakeStore) SetSessionReviewActive(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *fakeStore) ClearSessionReview(_ context.Context, _, _ string) error {
	return nil
}

func (s *fakeStore) ReadReview(id string) (*review.Review, error) {
	if err := review.ValidID(id); err != nil {
		return nil, err
	}
	s.reviewsMu.Lock()
	defer s.reviewsMu.Unlock()
	r, ok := s.reviews[id]
	if !ok {
		return nil, fmt.Errorf("review %q not found", id)
	}
	cp := *r
	if r.Comments != nil {
		cp.Comments = append([]review.ReviewComment(nil), r.Comments...)
	}
	return &cp, nil
}

// submitReview is a test-only helper: it mutates the in-memory review to
// completed state, mirroring what a real Wails Submit binding would do.
func (s *fakeStore) submitReview(id, event, body string, comments []review.ReviewComment) {
	s.reviewsMu.Lock()
	defer s.reviewsMu.Unlock()
	r, ok := s.reviews[id]
	if !ok {
		return
	}
	now := time.Now()
	r.Status = review.ReviewComplete
	r.Completed = &now
	r.Event = event
	r.Body = body
	r.Comments = comments
}

// fakeResolver maps session IDs to idea slugs.
type fakeResolver struct {
	mapping map[string]string
	// running marks UUIDs the orchestration tests want considered live.
	running map[string]bool
	// replays maps UUID → snapshot bytes; orchestration tests drive the
	// get_session_output handler against canned output.
	replays map[string][]byte
	// snapshots maps "slug/uuid" → persisted dormant snapshot bytes used
	// by tests covering get_session_output's dormant fallback path.
	snapshots map[string][]byte
	// writes captures bytes the orchestrator sent to each UUID so
	// send_session_input tests can assert the wire format without a
	// real PTY.
	writesMu sync.Mutex
	writes   map[string][][]byte
}

func (r *fakeResolver) GetIdeaSlug(uuid string) (string, error) {
	slug, ok := r.mapping[uuid]
	if !ok {
		return "", fmt.Errorf("session %q not found", uuid)
	}
	return slug, nil
}

func (r *fakeResolver) IsRunning(uuid string) bool {
	if r.running != nil {
		return r.running[uuid]
	}
	_, ok := r.replays[uuid]
	if ok {
		return true
	}
	_, ok = r.mapping[uuid]
	return ok
}

func (r *fakeResolver) GetSessionReplay(uuid string) ([]byte, error) {
	if data, ok := r.replays[uuid]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("session %q not running", uuid)
}

func (r *fakeResolver) ReadSessionSnapshot(slug, uuid string) ([]byte, error) {
	if data, ok := r.snapshots[slug+"/"+uuid]; ok {
		return data, nil
	}
	return nil, nil
}

func (r *fakeResolver) Write(uuid string, data []byte) error {
	r.writesMu.Lock()
	defer r.writesMu.Unlock()
	if r.writes == nil {
		r.writes = make(map[string][][]byte)
	}
	r.writes[uuid] = append(r.writes[uuid], append([]byte(nil), data...))
	return nil
}

func setupManager(t *testing.T) (*Manager, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.ideas["test-idea"] = &model.Idea{
		Slug:   "test-idea",
		Name:   "Test Idea",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/owner/repo/pull/1", Label: "Core PR"},
		},
	}

	resolver := &fakeResolver{mapping: map[string]string{
		"ses-1-test": "test-idea",
	}}

	m := NewManager(store, resolver, nil)
	return m, store
}

// captureEvents subscribes to the manager's event broker (creating
// one if the manager was constructed without one) and returns a
// drain func the test calls AFTER the code under test has fired its
// emits. Drain is non-blocking — it pulls everything currently
// buffered on the subscription channel and returns the event names
// in publish order. Use for assertions like slices.Contains where
// order doesn't matter; for sequenced assertions, prefer reading
// the channel directly via the broker.
func captureEvents(t *testing.T, m *Manager) func() []string {
	t.Helper()
	if m.events == nil {
		m.events = pubsub.New[pubsub.Event]()
	}
	ch, cancel := m.events.Subscribe()
	t.Cleanup(cancel)
	return func() []string {
		var names []string
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return names
				}
				names = append(names, ev.Name)
			default:
				return names
			}
		}
	}
}

func TestListResources(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	handler := m.handleListResources("ses-1-test")
	result, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	// Parse the text content.
	text := result.Content[0].(mcp.TextContent).Text
	var resources []model.Resource
	if err := json.Unmarshal([]byte(text), &resources); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].Label != "Core PR" {
		t.Errorf("label = %q, want %q", resources[0].Label, "Core PR")
	}
}

func TestAddResource(t *testing.T) {
	t.Parallel()
	m, store := setupManager(t)

	handler := m.handleAddResource("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"type":  "notion",
		"url":   "https://notion.so/page",
		"label": "Design doc",
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	// Verify store was updated.
	idea := store.ideas["test-idea"]
	if len(idea.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(idea.Resources))
	}
	added := idea.Resources[1]
	if added.Type != "notion" || added.Label != "Design doc" {
		t.Errorf("added resource = %+v", added)
	}

	// Verify history appended.
	if len(store.history) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(store.history))
	}
	if store.history[0].Event != "resource_added" {
		t.Errorf("event = %q, want %q", store.history[0].Event, "resource_added")
	}
}

func TestAddResourceDedupeByURL(t *testing.T) {
	t.Parallel()
	m, store := setupManager(t)

	handler := m.handleAddResource("ses-1-test")

	// First call: add as "web" type.
	req1 := mcp.CallToolRequest{}
	req1.Params.Arguments = map[string]any{
		"type":  "web",
		"url":   "https://github.com/owner/repo/pull/42",
		"label": "PR link",
	}
	result1, err := handler(context.Background(), req1)
	if err != nil {
		t.Fatalf("first add error: %v", err)
	}
	if result1.IsError {
		t.Fatalf("first add tool error: %v", result1.Content)
	}

	// Second call: same URL, richer type and updated label.
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]any{
		"type":  "github_pr",
		"url":   "https://github.com/owner/repo/pull/42",
		"label": "PR #42",
	}
	result2, err := handler(context.Background(), req2)
	if err != nil {
		t.Fatalf("second add error: %v", err)
	}
	if result2.IsError {
		t.Fatalf("second add tool error: %v", result2.Content)
	}

	// The pre-existing "Core PR" resource plus exactly one for the new URL.
	idea := store.ideas["test-idea"]
	if len(idea.Resources) != 2 {
		t.Fatalf("expected 2 resources (deduped), got %d: %+v", len(idea.Resources), idea.Resources)
	}

	// Find the upserted resource.
	var upserted *model.Resource
	for i := range idea.Resources {
		if idea.Resources[i].URL == "https://github.com/owner/repo/pull/42" {
			upserted = &idea.Resources[i]
			break
		}
	}
	if upserted == nil {
		t.Fatal("upserted resource not found")
	}
	if upserted.Type != "github_pr" {
		t.Errorf("type = %q, want github_pr (type promotion)", upserted.Type)
	}
	if upserted.Label != "PR #42" {
		t.Errorf("label = %q, want PR #42", upserted.Label)
	}
}

func TestUpdateIdea(t *testing.T) {
	t.Parallel()
	m, store := setupManager(t)

	handler := m.handleUpdateIdea("ses-1-test")
	req := mcp.CallToolRequest{}
	// status field is no longer accepted — use pause_idea / archive_idea / etc.
	req.Params.Arguments = map[string]any{
		"summary": "Updated description",
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	idea := store.ideas["test-idea"]
	if idea.Body != "Updated description" {
		t.Errorf("summary = %q, want %q", idea.Body, "Updated description")
	}
}

func TestUpdateIdea_RejectsStatusField(t *testing.T) {
	t.Parallel()
	m, store := setupManager(t)

	handler := m.handleUpdateIdea("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"status": "archived"}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true, got success: %v", result.Content)
	}
	body := ""
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			body = tc.Text
		}
	}
	for _, want := range []string{"archive_idea", "unarchive_idea", "pause_idea", "resume_idea"} {
		if !strings.Contains(body, want) {
			t.Errorf("rejection missing %q in: %q", want, body)
		}
	}
	if idea := store.ideas["test-idea"]; idea.Status == model.StatusArchived {
		t.Errorf("status was mutated to archived despite rejection")
	}
}

func TestUpdateIdeaNoChanges(t *testing.T) {
	t.Parallel()
	m, _ := setupManager(t)

	handler := m.handleUpdateIdea("ses-1-test")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError {
		t.Error("expected no error for empty update")
	}
	text := result.Content[0].(mcp.TextContent).Text
	if text != "No changes specified" {
		t.Errorf("text = %q, want %q", text, "No changes specified")
	}
}

func TestEventFnCalled(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["test-idea"] = &model.Idea{
		Slug:   "test-idea",
		Name:   "Test",
		Status: model.StatusActive,
	}
	resolver := &fakeResolver{mapping: map[string]string{"ses-1": "test-idea"}}

	br := pubsub.New[pubsub.Event]()
	ch, _ := br.Subscribe()

	m := NewManager(store, resolver, br)
	handler := m.handleAddResource("ses-1")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"type":  "doc",
		"label": "test",
	}
	_, _ = handler(context.Background(), req)

	select {
	case ev := <-ch:
		if ev.Name != "idea:resource_added" {
			t.Errorf("event name = %q, want idea:resource_added", ev.Name)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for emit")
	}
}

// Ensure fakeStore satisfies IdeaStore at compile time.
var (
	_ IdeaStore       = (*fakeStore)(nil)
	_ SessionResolver = (*fakeResolver)(nil)
	_                 = time.Now // suppress unused import warning
)
