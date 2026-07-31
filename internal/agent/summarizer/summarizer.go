// Package summarizer regenerates idea-level summary sidecars from
// the most recent session's transcript tail.
//
// A Summarizer owns a bounded worker pool that pulls slugs off an
// in-memory queue, finds the slug's latest session, reads the last
// few turns of that session's Claude transcript, feeds them through
// a configurable [Generator] (deterministic snippet, headless Claude,
// headless Codex, …), and persists the result via [Store.WriteSummary].
//
// Triggers (SessionEnd hook, idea:changed debounce, periodic sweep)
// live in callers — this package just owns the regenerate-one-idea
// pipeline + the pool that runs it.
package summarizer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/agent/transcript/claudefmt"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

// Store is the slice of the FSStore API the summarizer needs. Kept
// minimal so tests can substitute a fake without standing up the
// full store. Idea.Body carries the idea.md body (per the
// existing model convention) — no separate body fetch needed.
type Store interface {
	Get(ctx context.Context, slug string) (*model.Idea, error)
	ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error)
	// ListRepos returns linked worktrees for an idea so the headless
	// summarizer can hand the agent absolute paths for `git log`
	// research. May return (nil, nil) when no repos are linked.
	ListRepos(ctx context.Context, slug string) ([]store.RepoLink, error)
	WriteSummary(ctx context.Context, slug string, sum model.Summary) error
	// AddResource dedupes and persists a resource on the idea. The
	// concrete *service.IdeaService already satisfies this; the summarizer
	// calls it best-effort after writing the summary sidecar.
	AddResource(ctx context.Context, slug string, res model.Resource) error
}

// Summarizer runs the pipeline that turns "idea + latest session
// transcript" into a one-line summary sidecar.
//
// Use [New] to construct, then [Start] before any [Enqueue] calls.
// [Stop] drains in-flight work and tears down the workers.
type Summarizer struct {
	generator   Generator
	store       Store
	projectsDir string
	// ideasDir is the parent of every idea's directory tree
	// (<ideasDir>/<slug>/...). Used to compute IdeaDir and the
	// vscreen snapshot path for the headless generator. Empty
	// disables those pointers — the generator still works on
	// name+body+transcript alone.
	ideasDir string

	// transcriptTurns is the number of trailing turns from the
	// latest session passed to the generator. Empirical sweet spot
	// is small — the line-1 summary is "what's this about", not
	// "summarize the whole thing".
	transcriptTurns int

	// queue carries slugs awaiting regeneration. Workers pull off
	// this channel; coalescing prevents duplicate entries.
	queue chan string

	// pendingMu guards pending; pending holds slugs currently queued
	// or in-flight so Enqueue is idempotent within a short window.
	pendingMu sync.Mutex
	pending   map[string]bool

	// wg tracks worker goroutines; Stop blocks on it.
	wg sync.WaitGroup

	// startCtx + cancel control the workers' lifetime.
	startCtx context.Context
	cancel   context.CancelFunc

	logger *slog.Logger
}

// New constructs a Summarizer. The returned instance is idle —
// call [Start] before [Enqueue]. Caller owns the [Stop] lifecycle.
//
// gen selects the backend (snippet / headless-claude / headless-codex
// / …); callers wire it from the config-driven picker in App.
func New(gen Generator, store Store, opts ...Option) *Summarizer {
	s := &Summarizer{
		generator:       gen,
		store:           store,
		transcriptTurns: 12,
		queue:           make(chan string, 64),
		pending:         make(map[string]bool),
		logger:          slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option is the functional-options shape for [New].
type Option func(*Summarizer)

// WithProjectsDir sets the Claude transcripts root (typically
// ~/.claude/projects). Required for the runner to locate session
// transcripts; tests that don't touch the FS can leave it empty and
// supply a fake runner that doesn't read transcripts.
func WithProjectsDir(p string) Option {
	return func(s *Summarizer) { s.projectsDir = p }
}

// WithIdeasDir sets the parent of every idea's directory tree so the
// summarizer can compute IdeaDir + vscreen snapshot paths for the
// headless generator. Empty leaves those pointers off — generators
// still work on name+body+transcript alone.
func WithIdeasDir(p string) Option {
	return func(s *Summarizer) { s.ideasDir = p }
}

// WithTranscriptTurns overrides the default (12) turn count read
// from the tail of the latest session.
func WithTranscriptTurns(n int) Option {
	return func(s *Summarizer) {
		if n > 0 {
			s.transcriptTurns = n
		}
	}
}

// WithLogger swaps the slog logger (default: slog.Default).
func WithLogger(l *slog.Logger) Option {
	return func(s *Summarizer) {
		if l != nil {
			s.logger = l
		}
	}
}

// Start launches workers. Idempotent on repeat calls. parent is the
// owning context; cancelling it tears down the pool. workers <= 0
// defaults to 2 (enough to amortize bursts of SessionEnds without
// fanning out to N subprocesses).
func (s *Summarizer) Start(parent context.Context, workers int) {
	if s.cancel != nil {
		return
	}
	if workers <= 0 {
		workers = 2
	}
	s.startCtx, s.cancel = context.WithCancel(parent)
	for range workers {
		s.wg.Add(1)
		go s.workerLoop()
	}
}

// Stop drains in-flight work and waits for workers to exit. Safe to
// call after Start; no-op before Start. Blocks until queued items
// either run or are dropped due to ctx cancellation.
func (s *Summarizer) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
	s.cancel = nil
}

// Enqueue schedules slug for regeneration. Duplicates are coalesced:
// if slug is already queued (or running), this is a no-op. Returns
// false if the queue is full (in which case the caller should log;
// the staleness sweep will pick it up later).
//
// The synthetic OrchestratorSlug is filtered here so hook handlers
// and sweeps can pass session.IdeaSlug verbatim without each caller
// having to special-case the orchestrator. The orchestrator has no
// idea.md and nothing summarizable; enqueueing it would just produce
// a "loading idea: ... no such file" warn line every cycle.
func (s *Summarizer) Enqueue(slug string) bool {
	if slug == "" || slug == model.OrchestratorSlug {
		return false
	}
	s.pendingMu.Lock()
	if s.pending[slug] {
		s.pendingMu.Unlock()
		return true
	}
	s.pending[slug] = true
	s.pendingMu.Unlock()

	select {
	case s.queue <- slug:
		return true
	default:
		s.pendingMu.Lock()
		delete(s.pending, slug)
		s.pendingMu.Unlock()
		s.logger.Warn("summarizer queue full, dropping",
			slog.String("slug", slug))
		return false
	}
}

func (s *Summarizer) workerLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.startCtx.Done():
			return
		case slug := <-s.queue:
			s.runJob(slug)
		}
	}
}

func (s *Summarizer) runJob(slug string) {
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, slug)
		s.pendingMu.Unlock()
	}()
	if err := s.regenerate(s.startCtx, slug); err != nil {
		s.logger.Warn("summarizer regenerate failed",
			slog.String("slug", slug),
			slog.Any("err", err))
	}
}

// regenerate is the per-slug pipeline: idea + latest session →
// transcript tail → prompt → headless run → sidecar write.
//
// Defense-in-depth skip for the orchestrator: Enqueue filters this
// slug at the front door, but any future direct-call path lands here
// and would otherwise emit a noisy "loading idea: no such file" warn.
func (s *Summarizer) regenerate(ctx context.Context, slug string) error {
	if slug == model.OrchestratorSlug {
		return nil
	}
	idea, err := s.store.Get(ctx, slug)
	if err != nil {
		return fmt.Errorf("loading idea: %w", err)
	}
	if idea == nil {
		return errors.New("idea not found")
	}
	body := idea.Body

	sessions, err := s.store.ListSessions(ctx, slug)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	latest := pickLatestEndedSession(sessions)
	transcriptTail := ""
	transcriptPath := ""
	if latest != nil && s.projectsDir != "" {
		transcriptPath = filepath.Join(s.projectsDir,
			claudefmt.EncodeProjectDir(latest.WorkingDir),
			latest.UUID+".jsonl")
		tail, err := claudefmt.LastNTurns(transcriptPath, s.transcriptTurns)
		if err == nil {
			transcriptTail = tail
		} else {
			s.logger.Debug("summarizer transcript read failed",
				slog.String("path", transcriptPath),
				slog.Any("err", err))
		}
	}

	ideaDir := s.ideaDir(slug)
	repoPaths := s.collectRepoPaths(ctx, slug, ideaDir)
	vscreenPath := s.vscreenPath(slug, latest)

	result, err := s.generator.Generate(ctx, GenerateInput{
		IdeaName:       idea.Name,
		IdeaBody:       body,
		TranscriptTail: transcriptTail,
		SessionUUID:    sessionUUID(latest),
		// Phase / Type are GenerateInput fields for future use —
		// model.Idea doesn't carry them yet (see internal/model/idea.go).
		Status:         string(idea.Status),
		IdeaDir:        ideaDir,
		RepoPaths:      repoPaths,
		TranscriptPath: transcriptPath,
		VscreenPath:    vscreenPath,
	})
	if errors.Is(err, ErrEmpty) {
		return nil // nothing to write, dashboard falls back to body
	}
	if err != nil {
		return fmt.Errorf("generator: %w", err)
	}
	line := sanitizeSummary(result.Line)
	if line == "" {
		return nil
	}

	sum := model.Summary{
		Line:        line,
		GeneratedAt: time.Now().UTC(),
	}
	if latest != nil {
		sum.SourceSessionUUID = latest.UUID
		if latest.Ended != nil {
			t := latest.Ended.UTC()
			sum.SourceSessionEndedAt = &t
		}
	}
	if err := s.store.WriteSummary(ctx, slug, sum); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}

	// Apply suggested resources best-effort: log warn on failure but
	// do not propagate and do not skip remaining entries.
	for _, res := range result.SuggestedResources {
		if err := s.store.AddResource(ctx, slug, res); err != nil {
			s.logger.Warn("summarizer: add suggested resource failed",
				slog.String("slug", slug),
				slog.String("url", res.URL),
				slog.Any("err", err))
		}
	}
	return nil
}

// ideaDir resolves <ideasDir>/<slug>; "" when no ideasDir is wired.
func (s *Summarizer) ideaDir(slug string) string {
	if s.ideasDir == "" {
		return ""
	}
	return filepath.Join(s.ideasDir, slug)
}

// collectRepoPaths joins each linked repo's RepoLink.Path onto the
// idea root to produce absolute paths the headless agent can `git
// log` against. Returns nil when ideaDir is empty or ListRepos
// fails — failure is non-blocking (logged at debug, summary still
// runs).
func (s *Summarizer) collectRepoPaths(ctx context.Context, slug, ideaDir string) []string {
	if ideaDir == "" {
		return nil
	}
	repos, err := s.store.ListRepos(ctx, slug)
	if err != nil {
		s.logger.Debug("summarizer list repos failed",
			slog.String("slug", slug),
			slog.Any("err", err))
		return nil
	}
	if len(repos) == 0 {
		return nil
	}
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, filepath.Join(ideaDir, r.Path))
	}
	return out
}

// vscreenPath resolves the per-session ANSI snapshot path. Shape
// matches internal/app/vscreen_persist.go:17-22:
// <ideaDir>/sessions/<uuid>.vscreen.ansi. Returns "" when there's no
// session to snapshot or ideasDir isn't wired.
func (s *Summarizer) vscreenPath(slug string, sess *model.AgentSession) string {
	if sess == nil || s.ideasDir == "" {
		return ""
	}
	return filepath.Join(s.ideasDir, slug, "sessions", sess.UUID+".vscreen.ansi")
}

// pickLatestEndedSession returns the most recently ended session
// (highest Ended timestamp). Running sessions are skipped — their
// transcript is mid-stream and not stable to summarize from.
// Returns nil when no qualifying session exists.
func pickLatestEndedSession(sessions []model.AgentSession) *model.AgentSession {
	candidates := make([]model.AgentSession, 0, len(sessions))
	for _, s := range sessions {
		if s.Status == model.SessionStatusRunning {
			continue
		}
		if s.Ended == nil {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Ended.After(*candidates[j].Ended)
	})
	return &candidates[0]
}

// summaryMaxBytes caps the persisted line. ~120 chars keeps the
// dashboard card honest about width; the runner's reply is trimmed
// to fit even if Haiku gets chatty.
const summaryMaxBytes = 240

// sanitizeSummary normalizes Haiku's output: trims whitespace, drops
// a single pair of wrapping quotes (Haiku sometimes does that), caps
// length. Runs are kept conservative — the prompt is the primary
// quality lever, this just defends against minor formatting drift.
func sanitizeSummary(s string) string {
	s = strings.TrimSpace(s)
	if n := len(s); n >= 2 && s[0] == '"' && s[n-1] == '"' {
		s = strings.TrimSpace(s[1 : n-1])
	}
	// Collapse internal newlines into spaces — the card renders one
	// line and we don't want stray breaks.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > summaryMaxBytes {
		s = strings.TrimSpace(s[:summaryMaxBytes])
	}
	return s
}

// sessionUUID returns the latest session's UUID or "".
func sessionUUID(s *model.AgentSession) string {
	if s == nil {
		return ""
	}
	return s.UUID
}

const defaultSystemPrompt = `You summarize software-project ideas in one declarative sentence.

VOICE
- Lead with the verb. Describe what's being done, not why.
- The idea body is the user's first-person notes ("I need", "I want", "my plan is to"). Rewrite that voice as declarative third-person. Do NOT use first-person pronouns (I, me, my, we, our, us). Do NOT quote the body verbatim.

SHAPE
- One single-line JSON object: {"summary": "...", "warnings": [...]}.
- summary: no quotes inside the value beyond what JSON requires, no labels, no surrounding markup. No period if the sentence is a fragment. Cap 120 chars.
- warnings: short strings naming missing or unusable inputs. Optional; omit the field or pass [] if every input was usable. NEVER describe missing context inside summary — warnings is the only channel for that.

INPUT PRIORITY
- Lifecycle metadata (status / phase / type) anchors the verb tense and shape: "Investigating ...", "Drafting ...", "Stuck on review of ...", "Shipping ...".
- The body says what the user wants to do; the transcript and the listed repo / vscreen / transcript paths show what has actually happened.
- When body and progress evidence disagree, lead with the evidence.`
