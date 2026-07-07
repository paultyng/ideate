package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/repo"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/store"
)

// sessionCoordinator is the subset of agent.AgentCoordinator that
// IdeaService uses. Defined here so tests can inject a fake.
type sessionCoordinator interface {
	List() []agent.SessionInfo
	Stop(ctx context.Context, uuid string) error
}

// Sentinel errors for lifecycle operations.
var (
	ErrNotArchived    = errors.New("idea is not archived")
	ErrNotPaused      = errors.New("idea is not paused")
	ErrAlreadyInState = errors.New("idea is already in the target state")
	// ErrIdeaArchived is returned by work-producing operations (LinkRepo,
	// session start) when the idea is archived. Cleanup ops (UnlinkRepo,
	// resource add/delete, metadata update) deliberately don't gate so
	// archived ideas remain prunable.
	ErrIdeaArchived = errors.New("idea is archived; unarchive before starting new work")
)

// IdeaService is the business-logic funnel for non-UI callers (MCP,
// hooks, CLI, summarizer). It satisfies mcp.IdeaStore + hooks.SessionStore
// + summarizer.Store, with idea/session reads + writes delegated to
// FSStore and the lifecycle methods coordinating Store + Coordinator.
//
// Owned policy: lifecycle transitions (Archive/Unarchive/Pause/Resume)
// with the collect-then-validate-then-mutate guarantee, state gating
// (LinkRepo + EnsureStartable refuse on archived; cleanup ops don't),
// LinkRepo auto-tracking the worktree origin as a `repo` resource,
// AddResource driving model.UpsertResource (canonical-URL dedupe +
// type promotion), and MarkSessionActive auto-flipping paused→active
// on first session start.
type IdeaService struct {
	store *store.FSStore
	coord sessionCoordinator
}

// New constructs an IdeaService. coord may be nil for callers that
// only use store-backed methods (e.g. tests).
func New(s *store.FSStore, c sessionCoordinator) *IdeaService {
	return &IdeaService{store: s, coord: c}
}

// List delegates to FSStore.List.
func (s *IdeaService) List(ctx context.Context) ([]model.Idea, error) {
	return s.store.List(ctx)
}

// Get delegates to FSStore.Get.
func (s *IdeaService) Get(ctx context.Context, slug string) (*model.Idea, error) {
	return s.store.Get(ctx, slug)
}

// Create delegates to FSStore.Create.
func (s *IdeaService) Create(ctx context.Context, idea *model.Idea) error {
	return s.store.Create(ctx, idea)
}

// Update delegates to FSStore.Update.
func (s *IdeaService) Update(ctx context.Context, idea *model.Idea) error {
	return s.store.Update(ctx, idea)
}

// AppendHistory delegates to FSStore.AppendHistory.
func (s *IdeaService) AppendHistory(ctx context.Context, slug string, event model.HistoryEvent) error {
	return s.store.AppendHistory(ctx, slug, event)
}

// ResolveRepoPath delegates to FSStore.ResolveRepoPath.
func (s *IdeaService) ResolveRepoPath(ctx context.Context, slug, repoPath string) (string, error) {
	return s.store.ResolveRepoPath(ctx, slug, repoPath)
}

// LinkRepo establishes the per-idea worktree and auto-tracks the repository's
// origin URL as a "repo" resource on the idea. The resource add is best-effort:
// failures are logged but do not fail the operation.
func (s *IdeaService) LinkRepo(ctx context.Context, slug, repoPath, branch, nameOverride string) (string, error) {
	// State gate: archived ideas refuse new work.
	if idea, err := s.store.Get(ctx, slug); err == nil && idea.Status == model.StatusArchived {
		return "", ErrIdeaArchived
	}

	name, err := s.store.LinkRepo(ctx, slug, repoPath, branch, nameOverride)
	if err != nil {
		return "", err
	}

	canonical, err := filepath.Abs(repoPath)
	if err == nil {
		if real, err2 := filepath.EvalSymlinks(canonical); err2 == nil {
			canonical = real
		}
		origin, err2 := repo.OriginURL(ctx, canonical)
		if err2 == nil && origin != "" {
			if addErr := s.AddResource(ctx, slug, model.Resource{Type: "repo", URL: origin, Label: name}); addErr != nil {
				slog.Warn("auto-tracking repo resource",
					slog.String("slug", slug),
					slog.String("name", name),
					slog.Any("err", addErr))
			}
		}
	}

	return name, nil
}

// UnlinkRepo delegates to FSStore.UnlinkRepo.
func (s *IdeaService) UnlinkRepo(ctx context.Context, slug, name string, force bool) error {
	return s.store.UnlinkRepo(ctx, slug, name, force)
}

// ListRepos delegates to FSStore.ListRepos.
func (s *IdeaService) ListRepos(ctx context.Context, slug string) ([]store.RepoLink, error) {
	return s.store.ListRepos(ctx, slug)
}

// ListSessions delegates to FSStore.ListSessions.
func (s *IdeaService) ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error) {
	return s.store.ListSessions(ctx, slug)
}

// RenameIdea delegates to FSStore.RenameIdea.
func (s *IdeaService) RenameIdea(ctx context.Context, oldSlug, newSlug string) (*store.RenameResult, error) {
	return s.store.RenameIdea(ctx, oldSlug, newSlug)
}

// Delete delegates to FSStore.Delete.
func (s *IdeaService) Delete(ctx context.Context, slug string, force bool) error {
	return s.store.Delete(ctx, slug, force)
}

// CreateOrReopenDiffReview delegates to FSStore.CreateOrReopenDiffReview.
func (s *IdeaService) CreateOrReopenDiffReview(opts review.CreateOpts) (*review.Review, bool, error) {
	return s.store.CreateOrReopenDiffReview(opts)
}

// CreateOrReopenMarkdownReview delegates to FSStore.CreateOrReopenMarkdownReview.
func (s *IdeaService) CreateOrReopenMarkdownReview(opts review.MarkdownCreateOpts) (*review.Review, bool, error) {
	return s.store.CreateOrReopenMarkdownReview(opts)
}

// ReadReview delegates to FSStore.ReadReview.
func (s *IdeaService) ReadReview(id string) (*review.Review, error) {
	return s.store.ReadReview(id)
}

// CancelReview delegates to FSStore.CancelReview.
func (s *IdeaService) CancelReview(id string) (*review.Review, error) {
	return s.store.CancelReview(id)
}

// SetSessionReviewActive delegates to FSStore.SetSessionReviewActive.
func (s *IdeaService) SetSessionReviewActive(ctx context.Context, slug, uuid, reviewID string) error {
	return s.store.SetSessionReviewActive(ctx, slug, uuid, reviewID)
}

// ClearSessionReview delegates to FSStore.ClearSessionReview.
func (s *IdeaService) ClearSessionReview(ctx context.Context, slug, uuid string) error {
	return s.store.ClearSessionReview(ctx, slug, uuid)
}

// WriteSession delegates to FSStore.WriteSession.
func (s *IdeaService) WriteSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	return s.store.WriteSession(ctx, slug, key, session)
}

// UpdateSession delegates to FSStore.UpdateSession.
func (s *IdeaService) UpdateSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	return s.store.UpdateSession(ctx, slug, key, session)
}

// TouchIdea delegates to FSStore.TouchIdea.
func (s *IdeaService) TouchIdea(ctx context.Context, slug string) (time.Time, error) {
	return s.store.TouchIdea(ctx, slug)
}

// AddResource upserts res into the idea identified by slug, deduping
// by canonical URL and promoting the resource type when applicable.
func (s *IdeaService) AddResource(ctx context.Context, slug string, res model.Resource) error {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return fmt.Errorf("getting idea: %w", err)
	}
	model.UpsertResource(idea, res)
	return s.store.Update(ctx, idea)
}

// DeleteResource removes the first resource whose canonical URL matches url.
// Returns (true, nil) when a resource is removed, (false, nil) when no match
// is found (idempotent no-op).
func (s *IdeaService) DeleteResource(ctx context.Context, slug, url string) (bool, error) {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return false, fmt.Errorf("getting idea: %w", err)
	}
	targetKey := model.NormalizeResourceKey(model.Resource{URL: url})
	for i := range idea.Resources {
		if model.NormalizeResourceKey(idea.Resources[i]) != targetKey {
			continue
		}
		idea.Resources = append(idea.Resources[:i], idea.Resources[i+1:]...)
		if err := s.store.Update(ctx, idea); err != nil {
			return false, fmt.Errorf("updating idea: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// Archive stops any running sessions (if force), checks for dirty repos,
// releases repo resources, unlinks repos, and marks the idea archived.
//
// All validation (session gate + dirty-repo gate) is collected before any
// mutation fires, so a failure in ListRepos cannot leave sessions stopped
// while the idea is still active.
func (s *IdeaService) Archive(ctx context.Context, slug string, force bool) (*model.ArchiveReport, error) {
	// (a) Read idea; early exit if already archived.
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("getting idea: %w", err)
	}
	if idea.Status == model.StatusArchived {
		return nil, ErrAlreadyInState
	}

	// (b) Collect running sessions — gate only, no stopping yet.
	var running []string
	if s.coord != nil {
		for _, ses := range s.coord.List() {
			if ses.IdeaSlug == slug && ses.Status == agent.StatusRunning {
				running = append(running, ses.ID)
			}
		}
	}
	if len(running) > 0 && !force {
		return nil, fmt.Errorf("%w: session %s", store.ErrIdeaBusy, running[0])
	}

	// (c) Collect repos + dirty list — gate only, no unlinking yet.
	links, err := s.store.ListRepos(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("listing repos: %w", err)
	}
	var dirty []string
	for _, l := range links {
		if l.Dirty {
			dirty = append(dirty, l.Name)
		}
	}
	if len(dirty) > 0 && !force {
		return nil, &store.ErrDirtyRepos{Repos: dirty}
	}

	// (d) Collect open/in-progress backlog items — gate only. The
	// backlog is the idea's durable memory of in-flight work;
	// archiving without acknowledging it strips that memory silently.
	// Titles are capped to 10 so the error message stays bounded on
	// runaway backlogs; the caller uses Count to render the full total.
	items, err := s.store.ListBacklog(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("listing backlog: %w", err)
	}
	var openTitles []string
	openCount := 0
	for _, it := range items {
		if it.Status != model.BacklogStatusOpen && it.Status != model.BacklogStatusInProgress {
			continue
		}
		openCount++
		if len(openTitles) < 10 {
			openTitles = append(openTitles, it.Title)
		}
	}
	if openCount > 0 && !force {
		return nil, &store.ErrOpenBacklogItems{Titles: openTitles, Count: openCount}
	}

	// (e) All gates passed — mutate in order: stop sessions, release repos, flip status.
	for _, uuid := range running {
		if stopErr := s.coord.Stop(ctx, uuid); stopErr != nil {
			slog.Warn("stopping session during archive",
				slog.String("slug", slug),
				slog.String("session", uuid),
				slog.Any("err", stopErr))
		}
	}

	var released []model.Resource
	for _, l := range links {
		if l.OriginURL != "" {
			res := model.Resource{Type: "repo", URL: l.OriginURL, Label: l.Name}
			if addErr := s.AddResource(ctx, slug, res); addErr != nil {
				slog.Warn("auto-tracking repo resource on archive",
					slog.String("slug", slug),
					slog.String("name", l.Name),
					slog.Any("err", addErr))
			} else {
				released = append(released, res)
			}
		}
		if unlinkErr := s.store.UnlinkRepo(ctx, slug, l.Name, true); unlinkErr != nil {
			slog.Warn("unlinking repo during archive",
				slog.String("slug", slug),
				slog.String("name", l.Name),
				slog.Any("err", unlinkErr))
		}
	}

	// Re-read after resource mutations, then set archived.
	idea, err = s.store.Get(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("re-reading idea before archive: %w", err)
	}
	idea.Status = model.StatusArchived
	if err := s.store.Update(ctx, idea); err != nil {
		return nil, fmt.Errorf("updating idea: %w", err)
	}

	if err := s.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "archived",
	}); err != nil {
		slog.Warn("appending archived history", slog.String("slug", slug), slog.Any("err", err))
	}

	return &model.ArchiveReport{ReleasedRepos: released}, nil
}

// Unarchive transitions an archived idea back to active and returns its repo resources.
func (s *IdeaService) Unarchive(ctx context.Context, slug string) (*model.UnarchiveReport, error) {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("getting idea: %w", err)
	}
	if idea.Status != model.StatusArchived {
		return nil, ErrNotArchived
	}

	idea.Status = model.StatusActive
	if err := s.store.Update(ctx, idea); err != nil {
		return nil, fmt.Errorf("updating idea: %w", err)
	}

	if err := s.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "unarchived",
	}); err != nil {
		slog.Warn("appending unarchived history", slog.String("slug", slug), slog.Any("err", err))
	}

	var repos []model.Resource
	for _, r := range idea.Resources {
		if r.Type == "repo" {
			repos = append(repos, r)
		}
	}
	return &model.UnarchiveReport{RepoResources: repos}, nil
}

// Pause marks an idea as paused until the optional time.
func (s *IdeaService) Pause(ctx context.Context, slug string, until *time.Time) error {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return fmt.Errorf("getting idea: %w", err)
	}
	if idea.Status == model.StatusPaused {
		return ErrAlreadyInState
	}

	idea.Status = model.StatusPaused
	idea.PauseUntil = until
	if err := s.store.Update(ctx, idea); err != nil {
		return fmt.Errorf("updating idea: %w", err)
	}

	if err := s.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "paused",
	}); err != nil {
		slog.Warn("appending paused history", slog.String("slug", slug), slog.Any("err", err))
	}
	return nil
}

// MarkSessionActive transitions an idea from paused to active on first session start.
// No-op for ideas already active or archived. Returns nil if the idea is missing.
func (s *IdeaService) MarkSessionActive(ctx context.Context, slug string) error {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return nil // best-effort — missing idea is fine
	}
	if idea.Status == model.StatusPaused {
		idea.Status = model.StatusActive
		return s.store.Update(ctx, idea)
	}
	return nil
}

// EnsureStartable returns ErrIdeaArchived if the idea is archived,
// gating new + resume session starts at the App layer. Missing ideas
// pass through (nil) — let the downstream Start path produce the
// idea-not-found error in its own shape.
func (s *IdeaService) EnsureStartable(ctx context.Context, slug string) error {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return nil
	}
	if idea.Status == model.StatusArchived {
		return ErrIdeaArchived
	}
	return nil
}

// File I/O on idea dir

func (s *IdeaService) ListFiles(ctx context.Context, slug string) ([]string, error) {
	return s.store.ListFiles(ctx, slug)
}

func (s *IdeaService) ReadFile(ctx context.Context, slug, filename string) (string, error) {
	return s.store.ReadFile(ctx, slug, filename)
}

func (s *IdeaService) WriteFile(ctx context.Context, slug, filename, content string) error {
	return s.store.WriteFile(ctx, slug, filename, content)
}

func (s *IdeaService) ListRepoFiles(ctx context.Context, slug, repoName string) ([]string, error) {
	return s.store.ListRepoFiles(ctx, slug, repoName)
}

// History / summary

func (s *IdeaService) ReadHistory(ctx context.Context, slug string) ([]model.HistoryEvent, error) {
	return s.store.ReadHistory(ctx, slug)
}

func (s *IdeaService) ReadSummary(ctx context.Context, slug string) (*model.Summary, error) {
	return s.store.ReadSummary(ctx, slug)
}

func (s *IdeaService) WriteSummary(ctx context.Context, slug string, sum model.Summary) error {
	return s.store.WriteSummary(ctx, slug, sum)
}

// Session reads / writes

func (s *IdeaService) ReadSession(ctx context.Context, slug, key string) (*model.AgentSession, error) {
	return s.store.ReadSession(ctx, slug, key)
}

func (s *IdeaService) ListSessionSummaries(ctx context.Context) ([]store.IdeaSessionSummary, error) {
	return s.store.ListSessionSummaries(ctx)
}

func (s *IdeaService) FindRunningSession(ctx context.Context, slug, agentType string) (*model.AgentSession, error) {
	return s.store.FindRunningSession(ctx, slug, agentType)
}

func (s *IdeaService) WriteSessionPassive(ctx context.Context, slug, key string, session model.AgentSession) error {
	return s.store.WriteSessionPassive(ctx, slug, key, session)
}

// Review reads

func (s *IdeaService) ListPendingReviewsFull() ([]*review.Review, error) {
	return s.store.ListPendingReviewsFull()
}

// Review writes

func (s *IdeaService) SaveReviewDraft(id, body string, comments []review.ReviewComment) (*review.Review, error) {
	return s.store.SaveReviewDraft(id, body, comments)
}

func (s *IdeaService) SaveMarkdownReviewDraft(id, body, markedUp string) (*review.Review, error) {
	return s.store.SaveMarkdownReviewDraft(id, body, markedUp)
}

func (s *IdeaService) SubmitDiffReview(id, event, body string, comments []review.ReviewComment) (*review.Review, error) {
	return s.store.SubmitDiffReview(id, event, body, comments)
}

func (s *IdeaService) SubmitMarkdownReview(id, event, body, markedUp string) (*review.Review, error) {
	return s.store.SubmitMarkdownReview(id, event, body, markedUp)
}

// Resume clears pause state and returns the idea to active.
func (s *IdeaService) Resume(ctx context.Context, slug string) error {
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return fmt.Errorf("getting idea: %w", err)
	}
	if idea.Status != model.StatusPaused {
		return ErrNotPaused
	}

	idea.Status = model.StatusActive
	idea.PauseUntil = nil
	if err := s.store.Update(ctx, idea); err != nil {
		return fmt.Errorf("updating idea: %w", err)
	}

	if err := s.store.AppendHistory(ctx, slug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "resumed",
	}); err != nil {
		slog.Warn("appending resumed history", slog.String("slug", slug), slog.Any("err", err))
	}
	return nil
}

// ListBacklog delegates to FSStore. Returns the per-idea backlog
// items sorted oldest-first; missing backlog.json is the empty state.
func (s *IdeaService) ListBacklog(ctx context.Context, slug string) ([]model.BacklogItem, error) {
	return s.store.ListBacklog(ctx, slug)
}

// AddBacklogItem persists item to the idea's backlog. ID + timestamps
// are server-populated.
func (s *IdeaService) AddBacklogItem(ctx context.Context, slug string, item model.BacklogItem) (model.BacklogItem, error) {
	return s.store.AddBacklogItem(ctx, slug, item)
}

// UpdateBacklogItem patches the matching item. Non-empty fields on
// patch overwrite; empty fields leave existing values alone.
func (s *IdeaService) UpdateBacklogItem(ctx context.Context, slug, id string, patch model.BacklogItem) error {
	return s.store.UpdateBacklogItem(ctx, slug, id, patch)
}

// DeleteBacklogItem removes the item by id. Idempotent: (false, nil)
// when id is unknown.
func (s *IdeaService) DeleteBacklogItem(ctx context.Context, slug, id string) (bool, error) {
	return s.store.DeleteBacklogItem(ctx, slug, id)
}

// MatchResourceURLs delegates to FSStore. Returns a map keyed by the
// original input URL → list of ideas that reference it.
func (s *IdeaService) MatchResourceURLs(ctx context.Context, urls []string) (map[string][]store.ResourceMatch, error) {
	return s.store.MatchResourceURLs(ctx, urls)
}
