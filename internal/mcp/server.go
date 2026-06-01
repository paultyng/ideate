package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/paultyng/ideate/internal/claudecode"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/pubsub"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/store"
	"github.com/paultyng/ideate/internal/version"
)

// IdeaStore is the subset of store operations needed by the MCP server.
type IdeaStore interface {
	AddResource(ctx context.Context, slug string, res model.Resource) error
	// DeleteResource removes the resource matching url (by canonical URL).
	// Returns (true, nil) on removal; (false, nil) when not found (idempotent).
	DeleteResource(ctx context.Context, slug, url string) (bool, error)
	AppendHistory(ctx context.Context, slug string, event model.HistoryEvent) error
	Create(ctx context.Context, idea *model.Idea) error
	Get(ctx context.Context, slug string) (*model.Idea, error)
	List(ctx context.Context) ([]model.Idea, error)
	ResolveRepoPath(ctx context.Context, slug, repoPath string) (string, error)
	Update(ctx context.Context, idea *model.Idea) error

	LinkRepo(ctx context.Context, slug, repoPath, branch, nameOverride string) (string, error)
	UnlinkRepo(ctx context.Context, slug, name string, force bool) error
	ListRepos(ctx context.Context, slug string) ([]store.RepoLink, error)

	// Per-idea session enumeration — used by the orchestrator's
	// orchestration tools (`list_sessions`, `get_session`).
	ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error)

	// ReadSummary loads the headless-generated one-line summary sidecar
	// for an idea. Returns (nil, error) when no sidecar exists yet —
	// the caller treats that as "no summary available".
	ReadSummary(ctx context.Context, slug string) (*model.Summary, error)

	// Per-idea backlog. Mirrors the resources surface — items live
	// in <slug>/backlog.json, sorted oldest-first on read.
	ListBacklog(ctx context.Context, slug string) ([]model.BacklogItem, error)
	AddBacklogItem(ctx context.Context, slug string, item model.BacklogItem) (model.BacklogItem, error)
	UpdateBacklogItem(ctx context.Context, slug, id string, patch model.BacklogItem) error
	DeleteBacklogItem(ctx context.Context, slug, id string) (bool, error)

	// MatchResourceURLs answers "which ideas reference any of these
	// URLs?" in a single pass over the store. URLs are canonicalized
	// via model.NormalizeResourceKey before matching (same as the
	// add_resource dedupe path).
	MatchResourceURLs(ctx context.Context, urls []string) (map[string][]store.ResourceMatch, error)

	// RenameIdea moves an idea's directory and rewires session
	// WorkingDirs + linked worktrees. Returns the WorkingDir
	// transitions so the orchestrator tool can move Claude's
	// per-cwd transcript dirs separately.
	RenameIdea(ctx context.Context, oldSlug, newSlug string) (*store.RenameResult, error)

	// Delete removes an idea directory and any linked worktrees.
	// With force=false, refuses (via *store.ErrDirtyRepos) when any
	// linked worktree has uncommitted changes.
	Delete(ctx context.Context, slug string, force bool) error

	// Lifecycle operations — implemented by *service.IdeaService.
	Archive(ctx context.Context, slug string, force bool) (*model.ArchiveReport, error)
	Unarchive(ctx context.Context, slug string) (*model.UnarchiveReport, error)
	Pause(ctx context.Context, slug string, until *time.Time) error
	Resume(ctx context.Context, slug string) error

	// Review CRUD — used by request_*_review and get_*_review_result handlers.
	CreateOrReopenDiffReview(opts review.CreateOpts) (*review.Review, bool, error)
	CreateOrReopenMarkdownReview(opts review.MarkdownCreateOpts) (*review.Review, bool, error)
	ReadReview(id string) (*review.Review, error)
	CancelReview(id string) (*review.Review, error)

	// Per-session review state — flipped together with Activity so the
	// sidebar/global-bar can render reviewing distinctly from idle/active.
	SetSessionReviewActive(ctx context.Context, slug, uuid, reviewID string) error
	ClearSessionReview(ctx context.Context, slug, uuid string) error
}

// SessionResolver maps a running session's UUID to its idea slug and
// exposes PTY I/O for the orchestrator-only orchestration tools.
type SessionResolver interface {
	GetIdeaSlug(uuid string) (string, error)
	// IsRunning reports whether a session with this UUID is currently live.
	IsRunning(uuid string) bool
	// GetSessionReplay returns a vscreen snapshot for the given UUID:
	// ANSI bytes ready to be written into a fresh xterm.js instance.
	GetSessionReplay(uuid string) ([]byte, error)
	// ReadSessionSnapshot returns the persisted vscreen snapshot for a
	// dormant session. Slug and uuid identify the on-disk record.
	// Returns (nil, nil) when no snapshot file exists (e.g. session
	// crashed before the first periodic flush). Errors propagate only
	// for unexpected I/O failures.
	ReadSessionSnapshot(slug, uuid string) ([]byte, error)
	// Write sends bytes to the live session's PTY. Used by the
	// orchestrator-only send_session_input tool.
	Write(uuid string, data []byte) error
}

// Event names published to the app-wide event broker. Constants
// live here so MCP handlers and the App's bridge goroutine (which
// translates broker events into wailsRuntime.EventsEmit calls)
// don't drift on the wire string.
const (
	EventResourceAdded   = "idea:resource_added"
	EventResourceUpdated = "idea:resource_updated"
	EventResourceDeleted = "idea:resource_deleted"
	EventIdeaUpdated     = "idea:updated"
	EventIdeaCreated     = "idea:created"
	// EventIdeaRenamed fires when rename_idea moves an idea to a new
	// slug. Frontend uses it to redirect away from /idea/<old> if the
	// user has the renamed idea open.
	EventIdeaRenamed = "idea:renamed"
	// EventIdeaDeleted fires when delete_idea removes an idea. Frontend
	// uses it to redirect away from /idea/<slug> if the user is sitting
	// on the deleted idea's route.
	EventIdeaDeleted   = "idea:deleted"
	EventReviewCreated = "review:created"
	// EventReviewChanged fires whenever a review record's status changes
	// — created, submitted, cancelled, or swept by the startup pruner.
	// Frontend uses this as a coarse "the pending-reviews list may be
	// different now" signal so views like PendingReviewsBar can refetch
	// without polling on a tight interval.
	EventReviewChanged        = "review:changed"
	EventRepoChanged          = "repo:changed"
	EventOrchestratorNavigate = "orchestrator:navigate"
)

// Manager handles MCP server instances for agent sessions.
// Each session gets its own MCPServer instance so tool handlers
// can close over the session ID without context plumbing.
type Manager struct {
	store    IdeaStore
	resolver SessionResolver
	// events is the app-wide broker for frontend-bound events. Nil
	// is permitted for tests that don't care about emit traffic.
	events *pubsub.Broker[pubsub.Event]

	mu       sync.RWMutex
	sessions map[string]*server.StreamableHTTPServer

	// reviewBroker fans review-completion signals to long-poll
	// waiters in `request_*_review_result`. Each waiter subscribes
	// via pubsub.Filter on its specific review ID.
	reviewBroker *pubsub.Broker[review.Signal]

	// claudeProjectsDir is the directory Claude Code stores its
	// per-cwd transcript subdirs under (~/.claude/projects/ in
	// production, IDEATE_CLAUDE_PROJECTS_DIR in dev/test).
	// rename_idea uses it to migrate transcript dirs whose path key
	// changed because their session's WorkingDir moved. Empty =
	// transcript migration is skipped (acceptable in tests).
	claudeProjectsDir string

	// sleep gates the orchestrator's set_sleep_enabled tool. Wired
	// by App.Startup via SetSleepController; nil in tests that don't
	// care about the tool.
	sleep SleepController

	// skills exposes the canonical-skill bundle to the
	// list_default_skills / reset_default_skill orchestrator tools.
	// Wired by App.Startup via SetSkillsManager; nil disables the tools.
	skills SkillsManager

	// starter gates the orchestrator's start_idea_session tool. Wired
	// by App.Startup via SetSessionStarter; nil in tests that don't
	// care about the tool.
	starter SessionStarter
}

// NewManager creates a new MCP manager. events may be nil — emits
// turn into no-ops in that case.
func NewManager(store IdeaStore, resolver SessionResolver, events *pubsub.Broker[pubsub.Event]) *Manager {
	return &Manager{
		store:        store,
		resolver:     resolver,
		events:       events,
		sessions:     make(map[string]*server.StreamableHTTPServer),
		reviewBroker: pubsub.New[review.Signal](),
	}
}

// SetClaudeProjectsDir tells the manager where Claude's per-cwd
// transcript directories live. Called once on App.Startup. Empty =
// rename_idea skips the transcript-migration step (the rest of the
// rename still runs).
func (m *Manager) SetClaudeProjectsDir(dir string) {
	m.mu.Lock()
	m.claudeProjectsDir = dir
	m.mu.Unlock()
}

// emit publishes a frontend-bound event to the app-wide broker.
// No-op when the manager was constructed without a broker (tests
// that don't care about emit traffic pass nil).
func (m *Manager) emit(name string, data any) {
	if m.events == nil {
		return
	}
	m.events.Publish(pubsub.Event{Name: name, Data: data})
}

// RegisterSession creates an MCP server instance for a session bound to a
// specific idea. The session-scoped tools (`get_idea`, `add_resource`, etc.)
// resolve to the session's idea slug via the SessionResolver.
func (m *Manager) RegisterSession(sessionID string) {
	mcpServer := server.NewMCPServer(
		"ideate",
		version.Version,
		server.WithToolCapabilities(true),
	)

	m.addTools(mcpServer, sessionID)

	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/"),
	)

	m.mu.Lock()
	m.sessions[sessionID] = httpServer
	m.mu.Unlock()
}

// RegisterRootSession creates an MCP server instance for the root orchestrator
// session. The session has no implicit idea, so only the cross-idea tools
// (list_ideas, create_idea, *_by_slug) are exposed.
func (m *Manager) RegisterRootSession(sessionID string) {
	mcpServer := server.NewMCPServer(
		"ideate",
		version.Version,
		server.WithToolCapabilities(true),
	)

	m.addRootTools(mcpServer, sessionID)

	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/"),
	)

	m.mu.Lock()
	m.sessions[sessionID] = httpServer
	m.mu.Unlock()
}

// UnregisterSession removes the MCP server for a session.
func (m *Manager) UnregisterSession(sessionID string) {
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
}

// ValidateSession reports whether a session UUID is currently registered.
// Shared by the MCP and hooks servers so both endpoints authenticate
// against the same live-session registry.
func (m *Manager) ValidateSession(sessionID string) bool {
	m.mu.RLock()
	_, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	return ok
}

// ServeHTTP routes MCP requests to the correct per-session server.
// The session ID is read from the X-Ideate-Session-Id header.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sessionID := r.Header.Get(claudecode.SessionHeader)
	if sessionID == "" {
		slog.Warn("mcp: missing session header",
			slog.String("path", r.URL.Path), slog.String("remote", r.RemoteAddr))
		http.Error(w, "missing "+claudecode.SessionHeader+" header", http.StatusBadRequest)
		return
	}

	m.mu.RLock()
	handler, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		slog.Warn("mcp: unknown session",
			slog.String("session_uuid", sessionID),
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr))
		http.Error(w, fmt.Sprintf("unknown session: %s", sessionID), http.StatusNotFound)
		return
	}

	// Clone the request and rewrite path so the StreamableHTTPServer sees "/"
	// (its configured endpoint). Don't mutate the original request.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	handler.ServeHTTP(w, r2)

	slog.Debug("mcp: dispatched",
		slog.String("session_uuid", sessionID),
		slog.String("method", r.Method),
		slog.Duration("duration", time.Since(start)))
}

// NotifyReviewComplete signals any waiters that a review has been
// submitted or cancelled. Status mirrors the review's new on-disk
// state so subscribers can branch on submitted-vs-cancelled without
// re-reading the record (today's MCP consumer does re-read; future
// consumers like a topbar cancellation toast won't need to).
func (m *Manager) NotifyReviewComplete(reviewID string, status review.ReviewStatus) {
	m.reviewBroker.Publish(review.Signal{ID: reviewID, Status: status})
}

// waitForReview returns a channel that receives once when a review
// matching reviewID completes, plus a cancel func the caller must
// defer. The channel never blocks the publisher and is safe to
// abandon (cancel func tears down the filter goroutine cleanly).
func (m *Manager) waitForReview(reviewID string) (<-chan review.Signal, func()) {
	return pubsub.Filter(m.reviewBroker, func(s review.Signal) bool {
		return s.ID == reviewID
	})
}

func (m *Manager) addTools(s *server.MCPServer, sessionID string) {
	s.AddTool(getIdeaTool(), m.handleGetIdea(sessionID))
	s.AddTool(listResourcesTool(), m.handleListResources(sessionID))
	s.AddTool(addResourceTool(), m.handleAddResource(sessionID))
	s.AddTool(deleteResourceTool(), m.handleDeleteResource(sessionID))
	s.AddTool(updateIdeaTool(), m.handleUpdateIdea(sessionID))
	// Per-idea backlog. Current-idea variants on the per-session
	// surface; by-slug variants ride along via addCrossIdeaTools.
	s.AddTool(listBacklogTool(), m.handleListBacklog(sessionID))
	s.AddTool(addBacklogItemsTool(), m.handleAddBacklogItems(sessionID))
	s.AddTool(updateBacklogItemsTool(), m.handleUpdateBacklogItems(sessionID))
	s.AddTool(deleteBacklogItemsTool(), m.handleDeleteBacklogItems(sessionID))
	s.AddTool(requestDiffReviewTool(), m.handleRequestDiffReview(sessionID))
	s.AddTool(getDiffReviewResultTool(), m.handleGetDiffReviewResult(sessionID))
	s.AddTool(requestMarkdownReviewTool(), m.handleRequestMarkdownReview(sessionID))
	s.AddTool(getMarkdownReviewResultTool(), m.handleGetMarkdownReviewResult(sessionID))
	s.AddTool(cancelReviewTool(), m.handleCancelReview(sessionID))
	s.AddTool(linkRepoTool(), m.handleLinkRepo(sessionID))
	s.AddTool(listReposTool(), m.handleListRepos(sessionID))
	s.AddTool(unlinkRepoTool(), m.handleUnlinkRepo(sessionID))
	// reply_to_orchestrator is the per-idea-session counterpart of
	// the orchestrator's send_session_input — only available to
	// non-orchestrator (idea-bound) sessions.
	s.AddTool(replyToOrchestratorTool(), m.handleReplyToOrchestrator(sessionID))
	// Lifecycle tools available on session-scoped MCP — slug derived
	// from session. unarchive_idea is intentionally excluded: an
	// archived idea has no live sessions to bind to.
	s.AddTool(archiveIdeaTool(), m.handleArchiveIdea(sessionID))
	s.AddTool(pauseIdeaTool(), m.handlePauseIdea(sessionID))
	s.AddTool(resumeIdeaTool(), m.handleResumeIdea(sessionID))
	m.addCrossIdeaTools(s, sessionID)
}

// addRootTools wires the cross-idea (slug-based) tools onto the root
// orchestrator MCP server. No session-bound tools — the root has no implicit
// idea. Also exposes the orchestration tools (`list_sessions`,
// `get_session`, `get_session_output`) that are intentionally kept off
// the per-idea MCP surface.
func (m *Manager) addRootTools(s *server.MCPServer, sessionID string) {
	m.addCrossIdeaTools(s, sessionID)

	s.AddTool(listSessionsTool(), m.handleListSessions(sessionID))
	s.AddTool(getSessionTool(), m.handleGetSession(sessionID))
	s.AddTool(getSessionOutputTool(), m.handleGetSessionOutput(sessionID))
	s.AddTool(sendSessionInputTool(), m.handleSendSessionInput(sessionID))

	s.AddTool(gotoIdeaTool(), m.handleGotoIdea(sessionID))
	s.AddTool(gotoDashboardTool(), m.handleGotoDashboard(sessionID))
	s.AddTool(gotoSessionTool(), m.handleGotoSession(sessionID))

	s.AddTool(setSleepEnabledTool(), m.handleSetSleepEnabled())
	s.AddTool(startIdeaSessionTool(), m.handleStartIdeaSession())

	s.AddTool(listDefaultSkillsTool(), m.handleListDefaultSkills())
	s.AddTool(resetDefaultSkillTool(), m.handleResetDefaultSkill())

	// Lifecycle tools on the orchestrator — all four, each take an
	// explicit slug arg.
	s.AddTool(archiveIdeaTool(), m.handleArchiveIdea(sessionID))
	s.AddTool(unarchiveIdeaTool(), m.handleUnarchiveIdea(sessionID))
	s.AddTool(pauseIdeaTool(), m.handlePauseIdea(sessionID))
	s.AddTool(resumeIdeaTool(), m.handleResumeIdea(sessionID))
}

// addCrossIdeaTools registers the slug-based idea/resource management
// tools. They're pure data — store writes against named slugs, no PTY
// reach, no nav — so they're safe to expose on both per-idea and
// orchestrator MCP servers. An agent inside `idea-A` can spin off
// `idea-B` (via `create_idea`), inspect it (`get_idea_by_slug`), and
// link resources to it without leaving its own session.
func (m *Manager) addCrossIdeaTools(s *server.MCPServer, sessionID string) {
	s.AddTool(listIdeasTool(), m.handleListIdeas(sessionID))
	s.AddTool(createIdeaTool(), m.handleCreateIdea(sessionID))
	s.AddTool(getIdeaBySlugTool(), m.handleGetIdeaBySlug(sessionID))
	s.AddTool(updateIdeaBySlugTool(), m.handleUpdateIdeaBySlug(sessionID))
	s.AddTool(renameIdeaTool(), m.handleRenameIdea(sessionID))
	s.AddTool(deleteIdeaTool(), m.handleDeleteIdea(sessionID))
	s.AddTool(addResourceBySlugTool(), m.handleAddResourceBySlug(sessionID))
	s.AddTool(deleteResourceBySlugTool(), m.handleDeleteResourceBySlug(sessionID))
	s.AddTool(listResourcesBySlugTool(), m.handleListResourcesBySlug(sessionID))
	s.AddTool(linkRepoBySlugTool(), m.handleLinkRepoBySlug(sessionID))
	s.AddTool(unlinkRepoBySlugTool(), m.handleUnlinkRepoBySlug(sessionID))
	// By-slug backlog variants — let an agent in idea-A drop a task
	// on idea-B without bouncing through the orchestrator.
	s.AddTool(listBacklogBySlugTool(), m.handleListBacklogBySlug(sessionID))
	s.AddTool(addBacklogItemsBySlugTool(), m.handleAddBacklogItemsBySlug(sessionID))
	s.AddTool(updateBacklogItemsBySlugTool(), m.handleUpdateBacklogItemsBySlug(sessionID))
	s.AddTool(deleteBacklogItemsBySlugTool(), m.handleDeleteBacklogItemsBySlug(sessionID))
	// Bulk URL → ideas lookup. Read-only and pure data, safe on
	// both per-session and orchestrator surfaces via this helper.
	s.AddTool(matchResourceURLsTool(), m.handleMatchResourceURLs(sessionID))
}
