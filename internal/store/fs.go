package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/paultyng/ideate/internal/atomicfile"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/repo"
)

const (
	ideaFilename          = "idea.md"
	historyFile           = "history.jsonl"
	summaryFile           = "summary.json"
	backlogFile           = "backlog.json"
	reposDir              = "repos"
	sessionsDir           = "sessions"
	defaultTrackingBranch = "origin/main"
)

// FSStore implements Store using the local filesystem.
//
// branchPrefix, trackingBranch, and summaryBackend are sourced from
// <ideasDir>/config.json at construction and can be hot-swapped via
// ReloadConfig — the watcher invokes that path when the user (or an
// agent) edits config.json so changes don't require a restart. cfgMu
// guards reads/writes of those fields; everything else on the store
// is stateless (Get/List re-read from disk every call).
type FSStore struct {
	ideasDir       string
	reviewsDir     string
	cfgMu          sync.RWMutex
	branchPrefix   string
	trackingBranch string
	summaryBackend string

	// locks serializes in-process writers against the same idea's
	// on-disk artifacts (idea.md, backlog.json). See lock.go.
	locks *slugLockManager
}

// NewFSStore creates a new filesystem-backed store. ideasDir holds idea
// directories; reviewsDir is the central directory for review records (every
// review kind keyed by ID). trackingBranch is the upstream set on each idea's
// slug-named worktree branch; empty defaults to "origin/main".
func NewFSStore(ideasDir, reviewsDir, branchPrefix, trackingBranch string) *FSStore {
	if trackingBranch == "" {
		trackingBranch = defaultTrackingBranch
	}
	return &FSStore{
		ideasDir:       ideasDir,
		reviewsDir:     reviewsDir,
		branchPrefix:   branchPrefix,
		trackingBranch: trackingBranch,
		locks:          newSlugLockManager(),
	}
}

// branchPrefixVal returns the current branch-prefix snapshot under RLock.
func (s *FSStore) branchPrefixVal() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.branchPrefix
}

// trackingBranchVal returns the current tracking-branch snapshot under RLock.
func (s *FSStore) trackingBranchVal() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.trackingBranch
}

// ConfigSummaryBackend returns the configured summarizer backend
// name (empty / "snippet" / "claude" / "codex" / "testagent"). Empty
// is treated as snippet by callers. Re-reads under RLock so hot
// reloads pick up changes.
func (s *FSStore) ConfigSummaryBackend() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.summaryBackend
}

// SetSummaryBackend seeds the summary backend at construction time
// (from the same Config the caller passed branchPrefix/trackingBranch
// from). After this point ReloadConfig is the path that updates the
// value at runtime.
func (s *FSStore) SetSummaryBackend(backend string) {
	s.cfgMu.Lock()
	s.summaryBackend = backend
	s.cfgMu.Unlock()
}

// ReloadConfig re-reads <ideasDir>/config.json and atomically swaps the
// store's branch_prefix + tracking_branch. The watcher calls this when
// it sees a write to config.json; callers can also invoke it directly
// (e.g. an MCP tool that mutates config). No-op safety: a missing
// config.json reverts to defaults rather than erroring.
func (s *FSStore) ReloadConfig() error {
	cfg, err := LoadConfig(s.ideasDir)
	if err != nil {
		return err
	}
	tracking := cfg.TrackingBranch
	if tracking == "" {
		tracking = defaultTrackingBranch
	}
	s.cfgMu.Lock()
	s.branchPrefix = cfg.BranchPrefix
	s.trackingBranch = tracking
	s.summaryBackend = cfg.Summary.Backend
	s.cfgMu.Unlock()
	return nil
}

// ReviewsDir returns the central reviews directory.
func (s *FSStore) ReviewsDir() string { return s.reviewsDir }

func (s *FSStore) ideaDir(slug string) string {
	return filepath.Join(s.ideasDir, slug)
}

// trimSummaryPreview returns the leading n bytes of body, ending on a
// rune boundary and adding an ellipsis when truncated. Used by List()
// so dashboard cards get a preview without shipping multi-page bodies
// across IPC.
func trimSummaryPreview(body string, n int) string {
	if len(body) <= n {
		return body
	}
	out := body[:n]
	// Back up to a UTF-8 rune boundary so we don't truncate mid-rune.
	for len(out) > 0 && (out[len(out)-1]&0xC0) == 0x80 {
		out = out[:len(out)-1]
	}
	return out + "…"
}

// List reads all idea directories and returns ideas sorted by Updated (most recent first).
func (s *FSStore) List(_ context.Context) ([]model.Idea, error) {
	entries, err := os.ReadDir(s.ideasDir)
	if err != nil {
		return nil, fmt.Errorf("reading ideas dir: %w", err)
	}

	var ideas []model.Idea
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		// idea.md is the gate that distinguishes an Ideate idea
		// from any other dir under <ideasDir>. ParseSlug accepts
		// bare slugs (Phase A — date prefix is opportunistic on
		// collision, not required), so the file presence is the
		// only filter here.
		ideaPath := filepath.Join(s.ideaDir(slug), ideaFilename)
		data, err := os.ReadFile(ideaPath)
		if err != nil {
			continue
		}

		idea, err := model.ParseIdeaFile(string(data))
		if err != nil {
			continue
		}
		idea.Slug = slug
		// Frontmatter Created is authoritative when present (post-
		// Phase A default). Date-prefixed legacy slugs that pre-date
		// the frontmatter write fall through to the slug date.
		if idea.Created.IsZero() {
			if t, _, perr := model.ParseSlug(slug); perr == nil {
				idea.Created = t
			}
		}
		// Trim Summary to a card-sized preview so the dashboard idea
		// cards can render a snippet without shipping the full body
		// across IPC. The IdeaCard truncates further (140 chars after
		// whitespace flattening); 600 raw bytes leaves slack for
		// markdown/whitespace collapse.
		idea.Summary = trimSummaryPreview(idea.Summary, 600)
		ideas = append(ideas, *idea)
	}

	sort.Slice(ideas, func(i, j int) bool {
		ti := ideas[i].Updated
		if ti.IsZero() {
			ti = ideas[i].Created
		}
		tj := ideas[j].Updated
		if tj.IsZero() {
			tj = ideas[j].Created
		}
		return ti.After(tj)
	})

	return ideas, nil
}

// Get reads a single idea, including its full body and aggregated resources from all .md files.
func (s *FSStore) Get(_ context.Context, slug string) (*model.Idea, error) {
	dir := s.ideaDir(slug)
	ideaPath := filepath.Join(dir, ideaFilename)

	data, err := os.ReadFile(ideaPath)
	if err != nil {
		return nil, fmt.Errorf("reading idea.md: %w", err)
	}

	idea, err := model.ParseIdeaFile(string(data))
	if err != nil {
		return nil, err
	}

	idea.Slug = slug
	if idea.Created.IsZero() {
		if t, _, perr := model.ParseSlug(slug); perr == nil {
			idea.Created = t
		}
	}

	// Aggregate resources from other .md files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading idea dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == ideaFilename || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		mf, err := model.ParseMarkdownFile(string(content))
		if err != nil {
			continue
		}
		idea.Resources = append(idea.Resources, mf.Resources...)
	}

	return idea, nil
}

// Create writes a new idea to the filesystem.
func (s *FSStore) Create(_ context.Context, idea *model.Idea) error {
	now := time.Now()
	if idea.Updated.IsZero() {
		idea.Updated = now
	}

	slug := s.deriveCreateSlug(idea.Name, now)
	idea.Slug = slug
	idea.Created = now

	dir := s.ideaDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating idea dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, reposDir), 0o755); err != nil {
		return fmt.Errorf("creating repos dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, sessionsDir), 0o755); err != nil {
		return fmt.Errorf("creating sessions dir: %w", err)
	}

	content, err := model.SerializeIdeaFile(idea)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(dir, ideaFilename), []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing idea.md: %w", err)
	}

	return s.appendHistory(slug, model.HistoryEvent{
		Timestamp: now,
		Event:     "created",
	})
}

// deriveCreateSlug picks the directory name for a freshly-created idea.
// Tries the bare Slugify(name) first; if a directory by that name already
// exists, falls back to a date-prefixed form, then a date+time-prefixed
// form. The user only sees the date when there's an actual collision —
// the common case stays clean.
func (s *FSStore) deriveCreateSlug(name string, t time.Time) string {
	bare := model.Slugify(name)
	if bare != "" && !s.dirExists(bare) {
		return bare
	}
	withDate := model.GenerateSlug(name, t, false)
	if !s.dirExists(withDate) {
		return withDate
	}
	return model.GenerateSlug(name, t, true)
}

// dirExists reports whether <ideasDir>/<name> is an existing directory.
func (s *FSStore) dirExists(name string) bool {
	if name == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(s.ideasDir, name))
	return err == nil && info.IsDir()
}

// Update modifies an existing idea's frontmatter.
func (s *FSStore) Update(ctx context.Context, idea *model.Idea) error {
	unlock := s.locks.Lock(idea.Slug)
	defer unlock()
	return s.updateUnlocked(ctx, idea)
}

// updateUnlocked is the body of Update without the per-slug lock —
// callers that already hold s.locks.Lock(idea.Slug) (e.g. AddResource,
// DeleteResource) use this to avoid self-deadlock on the non-reentrant
// mutex. Public callers go through Update.
func (s *FSStore) updateUnlocked(_ context.Context, idea *model.Idea) error {
	dir := s.ideaDir(idea.Slug)
	ideaPath := filepath.Join(dir, ideaFilename)

	// Read existing to preserve body if Summary unchanged.
	data, err := os.ReadFile(ideaPath)
	if err != nil {
		return fmt.Errorf("reading existing idea.md: %w", err)
	}
	existing, err := model.ParseIdeaFile(string(data))
	if err != nil {
		return err
	}

	if idea.Summary == "" {
		idea.Summary = existing.Summary
	}
	idea.Updated = time.Now()

	content, err := model.SerializeIdeaFile(idea)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(ideaPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing idea.md: %w", err)
	}

	return s.appendHistory(idea.Slug, model.HistoryEvent{
		Timestamp: idea.Updated,
		Event:     "updated",
	})
}

// AddResource upserts res into the idea identified by slug. It delegates
// the dedupe-by-canonical-URL and type-promotion logic to model.UpsertResource.
func (s *FSStore) AddResource(ctx context.Context, slug string, res model.Resource) error {
	unlock := s.locks.Lock(slug)
	defer unlock()
	idea, err := s.Get(ctx, slug)
	if err != nil {
		return fmt.Errorf("getting idea: %w", err)
	}
	model.UpsertResource(idea, res)
	return s.updateUnlocked(ctx, idea)
}

// DeleteResource removes the first resource whose canonical URL matches url.
// Returns (true, nil) when removed; (false, nil) when no match (idempotent).
func (s *FSStore) DeleteResource(ctx context.Context, slug, url string) (bool, error) {
	unlock := s.locks.Lock(slug)
	defer unlock()
	idea, err := s.Get(ctx, slug)
	if err != nil {
		return false, fmt.Errorf("getting idea: %w", err)
	}
	targetKey := model.NormalizeResourceKey(model.Resource{URL: url})
	for i := range idea.Resources {
		if model.NormalizeResourceKey(idea.Resources[i]) != targetKey {
			continue
		}
		idea.Resources = append(idea.Resources[:i], idea.Resources[i+1:]...)
		if err := s.updateUnlocked(ctx, idea); err != nil {
			return false, fmt.Errorf("updating idea: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// TouchIdea bumps the Updated timestamp on idea.md without recording a
// history event. Used by session writes and hook-driven updates so the
// dashboard MRU sort reflects session activity without polluting history.
// Returns the new Updated value.
func (s *FSStore) TouchIdea(_ context.Context, slug string) (time.Time, error) {
	unlock := s.locks.Lock(slug)
	defer unlock()
	ideaPath := filepath.Join(s.ideaDir(slug), ideaFilename)
	data, err := os.ReadFile(ideaPath)
	if err != nil {
		// Synthetic slugs (orchestrator) own a sessions/ subdir but no
		// idea.md — TouchIdea on those is a no-op, not an error, so
		// session writes routed through WriteSession don't generate
		// log noise.
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("reading idea.md: %w", err)
	}
	idea, err := model.ParseIdeaFile(string(data))
	if err != nil {
		return time.Time{}, err
	}
	idea.Slug = slug
	idea.Updated = time.Now()

	content, err := model.SerializeIdeaFile(idea)
	if err != nil {
		return time.Time{}, err
	}
	if err := atomicfile.Write(ideaPath, []byte(content), 0o644); err != nil {
		return time.Time{}, fmt.Errorf("writing idea.md: %w", err)
	}
	return idea.Updated, nil
}

// Rename moves an idea directory to a new slug.
func (s *FSStore) Rename(_ context.Context, oldSlug, newSlug string) error {
	oldDir := s.ideaDir(oldSlug)
	newDir := s.ideaDir(newSlug)

	if _, err := os.Stat(oldDir); err != nil {
		return fmt.Errorf("old idea not found: %w", err)
	}
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("target slug %q already exists", newSlug)
	}

	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("renaming directory: %w", err)
	}

	return s.appendHistory(newSlug, model.HistoryEvent{
		Timestamp: time.Now(),
		Event:     "renamed",
		Fields:    map[string]any{"old_slug": oldSlug},
	})
}

// Delete removes an idea directory entirely. With force=false, it refuses
// when any linked worktree has uncommitted changes (returns *ErrDirtyRepos).
// With force=true, each worktree is removed via `git worktree remove --force`
// before the idea directory itself is deleted.
func (s *FSStore) Delete(ctx context.Context, slug string, force bool) error {
	links, err := s.ListRepos(ctx, slug)
	if err != nil {
		return fmt.Errorf("listing repos: %w", err)
	}

	if !force {
		var dirty []string
		for _, l := range links {
			if l.Dirty {
				dirty = append(dirty, l.Name)
			}
		}
		if len(dirty) > 0 {
			return &ErrDirtyRepos{Repos: dirty}
		}
	}

	for _, l := range links {
		wt := filepath.Join(s.ideaDir(slug), reposDir, l.Name)
		if err := repo.RemoveWorktree(ctx, wt, true); err != nil {
			slog.Warn("removing worktree during idea delete",
				slog.String("slug", slug), slog.String("name", l.Name), slog.Any("err", err))
		}
	}
	return os.RemoveAll(s.ideaDir(slug))
}

// ListRepoFiles returns top-level .md files inside the named worktree under
// the idea's repos dir.
func (s *FSStore) ListRepoFiles(_ context.Context, slug, repoName string) ([]string, error) {
	dir := filepath.Join(s.ideaDir(slug), reposDir, repoName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading repo dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		files = append(files, e.Name())
	}
	return files, nil
}

// ListFiles returns .md files in the idea directory, excluding idea.md.
func (s *FSStore) ListFiles(_ context.Context, slug string) ([]string, error) {
	dir := s.ideaDir(slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading idea dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == ideaFilename || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		files = append(files, e.Name())
	}
	return files, nil
}

// ReadFile reads a file inside the idea directory. filename is interpreted
// relative to <idea>; subpaths like "repos/<name>/README.md" are allowed,
// but the resolved path must stay under <idea>.
func (s *FSStore) ReadFile(_ context.Context, slug, filename string) (string, error) {
	p, err := s.resolveIdeaFilePath(slug, filename)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return string(data), nil
}

// WriteFile writes a file inside the idea directory. Path-traversal-safe.
func (s *FSStore) WriteFile(_ context.Context, slug, filename, content string) error {
	p, err := s.resolveIdeaFilePath(slug, filename)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(p, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	return nil
}

// resolveIdeaFilePath cleans filename and ensures the result stays under the
// idea's directory. Returns the absolute target path.
func (s *FSStore) resolveIdeaFilePath(slug, filename string) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	cleaned := filepath.Clean(filename)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("invalid filename: %q", filename)
	}
	root := s.ideaDir(slug)
	full := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid filename: %q", filename)
	}
	return full, nil
}

// AppendHistory adds an event to the idea's history log.
func (s *FSStore) AppendHistory(_ context.Context, slug string, event model.HistoryEvent) error {
	return s.appendHistory(slug, event)
}

func (s *FSStore) appendHistory(slug string, event model.HistoryEvent) error {
	p := filepath.Join(s.ideaDir(slug), historyFile)

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling history event: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening history file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing history event: %w", err)
	}
	return nil
}

// ReadHistory reads all events from the idea's history log.
func (s *FSStore) ReadHistory(_ context.Context, slug string) ([]model.HistoryEvent, error) {
	p := filepath.Join(s.ideaDir(slug), historyFile)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading history file: %w", err)
	}

	var events []model.HistoryEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev model.HistoryEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("parsing history line: %w", err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// LinkRepo creates a worktree of the canonical repo at repoPath under the
// idea's repos directory. The worktree's leaf name is auto-derived from the
// canonical's `origin` remote (or its basename), unless nameOverride is set.
// branch is checked out into the worktree (created if it doesn't exist
// locally); if branch is empty, falls back to <branchPrefix><slug>. Returns
// the resolved name.
func (s *FSStore) LinkRepo(ctx context.Context, slug, repoPath, branch, nameOverride string) (string, error) {
	if repoPath == "" {
		return "", fmt.Errorf("repo path is required")
	}

	canonical, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolving repo path: %w", err)
	}
	if real, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = real
	}
	if fi, err := os.Stat(canonical); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("repo path %q is not a directory", repoPath)
	}

	explicitName := nameOverride != ""
	name := nameOverride
	if name == "" {
		origin, _ := repo.OriginURL(ctx, canonical)
		name = repo.DeriveName(origin, canonical)
	}
	if name == "" {
		return "", fmt.Errorf("could not derive a name for repo at %q", repoPath)
	}

	// Two-tier collision check:
	//   - <idea>/repos/<name> — same idea linking the same canonical twice.
	//   - <canonical>/.git/worktrees/<name> — different idea already linked this
	//     canonical under the same leaf basename. Git keys worktree admin
	//     entries by leaf, not full path, so cross-idea collisions land here.
	// Auto-disambiguation only kicks in when the name was derived — explicit
	// overrides are honored verbatim and refuse on collision.
	worktreePath := filepath.Join(s.ideaDir(slug), reposDir, name)
	taken := func(candidate string) bool {
		if _, err := os.Stat(filepath.Join(s.ideaDir(slug), reposDir, candidate)); err == nil {
			return true
		}
		return repo.WorktreeAdminExists(canonical, candidate)
	}
	if taken(name) {
		if explicitName {
			return "", fmt.Errorf("repo %q is already linked", name)
		}
		// First retry: suffix with the idea slug. Then numeric tail (-2, -3, ...).
		base := name
		candidate := base + "-" + slug
		for i := 2; taken(candidate); i++ {
			candidate = fmt.Sprintf("%s-%s-%d", base, slug, i)
		}
		name = candidate
		worktreePath = filepath.Join(s.ideaDir(slug), reposDir, name)
	}

	tracking := s.trackingBranchVal()
	if branch == "" {
		branch = s.branchPrefixVal() + slug
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("creating repos dir: %w", err)
	}

	if err := repo.AddWorktree(ctx, canonical, worktreePath, branch); err != nil {
		return "", err
	}

	set, err := repo.SetUpstream(ctx, worktreePath, tracking, branch)
	if err != nil {
		return "", err
	}
	if !set {
		slog.Warn("tracking branch not found; worktree branch will have no upstream",
			slog.String("repo", repoPath),
			slog.String("branch", branch),
			slog.String("tracking", tracking))
	}

	return name, nil
}

// UnlinkRepo removes the worktree for name under the idea's repos dir. With
// force=false, refuses on uncommitted changes; force=true skips the check.
func (s *FSStore) UnlinkRepo(ctx context.Context, slug, name string, force bool) error {
	worktreePath := filepath.Join(s.ideaDir(slug), reposDir, name)
	if _, err := os.Stat(worktreePath); err != nil {
		return fmt.Errorf("worktree not found: %w", err)
	}

	if !force {
		st, err := repo.ReadStatus(ctx, worktreePath)
		if err != nil {
			return fmt.Errorf("reading worktree status: %w", err)
		}
		if st.Dirty {
			return &ErrDirtyRepos{Repos: []string{name}}
		}
	}

	return repo.RemoveWorktree(ctx, worktreePath, force)
}

// WriteSession persists an agent session record for an idea.
// The key is the session's stable agent session ID. Also bumps the idea's
// Updated timestamp so the dashboard MRU sort reflects session activity.
func (s *FSStore) WriteSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	if err := s.writeSessionFile(slug, key, session); err != nil {
		return err
	}
	if _, err := s.TouchIdea(ctx, slug); err != nil {
		// Session was persisted; surface the touch failure but don't undo.
		slog.Warn("touching idea after session write",
			slog.String("slug", slug), slog.String("session", key), slog.Any("err", err))
	}
	return nil
}

// WriteSessionPassive persists a session record without bumping the idea's
// Updated. See the Store interface comment for when to use it.
func (s *FSStore) WriteSessionPassive(_ context.Context, slug, key string, session model.AgentSession) error {
	return s.writeSessionFile(slug, key, session)
}

func (s *FSStore) writeSessionFile(slug, key string, session model.AgentSession) error {
	dir := filepath.Join(s.ideaDir(slug), sessionsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating sessions dir: %w", err)
	}
	p := filepath.Join(dir, key+".json")
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write(p, data, 0o644); err != nil {
		return fmt.Errorf("writing session file: %w", err)
	}
	return nil
}

// ReadSession reads a single agent session record by its stable key.
// repairSessionStatus coerces an unknown SessionStatus into running on read.
// Mirrors the inline pattern used for idea status in frontmatter.go: dormant
// is a recognized value (Task 2a writes it on idle-timeout / RSS-trigger
// finalize), so it must round-trip. A typo / older-schema status that doesn't
// match any known constant becomes running so the rest of the app doesn't
// have to defensively check.
func repairSessionStatus(session *model.AgentSession) {
	switch session.Status {
	case "", model.SessionStatusRunning, model.SessionStatusCompleted,
		model.SessionStatusStopped, model.SessionStatusFailed, model.SessionStatusDormant:
		return
	}
	slog.Debug("read-repaired unknown session status",
		slog.String("uuid", session.UUID),
		slog.String("got", string(session.Status)),
		slog.String("repaired_to", string(model.SessionStatusRunning)))
	session.Status = model.SessionStatusRunning
}

func (s *FSStore) ReadSession(_ context.Context, slug, key string) (*model.AgentSession, error) {
	p := filepath.Join(s.ideaDir(slug), sessionsDir, key+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading session file: %w", err)
	}
	var session model.AgentSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parsing session: %w", err)
	}
	repairSessionStatus(&session)
	return &session, nil
}

// ListSessions returns all agent session records for an idea, sorted by start time (most recent first).
func (s *FSStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	dir := filepath.Join(s.ideaDir(slug), sessionsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading sessions dir: %w", err)
	}

	var sessions []model.AgentSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var session model.AgentSession
		if err := json.Unmarshal(data, &session); err != nil {
			continue
		}
		repairSessionStatus(&session)
		sessions = append(sessions, session)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Started.After(sessions[j].Started)
	})

	return sessions, nil
}

// UpdateSession updates an existing agent session record.
func (s *FSStore) UpdateSession(ctx context.Context, slug, key string, session model.AgentSession) error {
	return s.WriteSession(ctx, slug, key, session)
}

// ReadSummary loads the idea's headless-generated summary sidecar.
// Returns (nil, nil) when no sidecar exists yet — that's the empty
// state, not an error.
func (s *FSStore) ReadSummary(_ context.Context, slug string) (*model.Summary, error) {
	p := filepath.Join(s.ideaDir(slug), summaryFile)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading summary: %w", err)
	}
	var sum model.Summary
	if err := json.Unmarshal(data, &sum); err != nil {
		return nil, fmt.Errorf("parsing summary: %w", err)
	}
	return &sum, nil
}

// WriteSummary persists the idea's headless-generated summary
// sidecar. Atomic; safe to call concurrently with ReadSummary.
func (s *FSStore) WriteSummary(_ context.Context, slug string, sum model.Summary) error {
	p := filepath.Join(s.ideaDir(slug), summaryFile)
	data, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling summary: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write(p, data, 0o644); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// SetSessionReviewActive sets ActiveReviewID and Activity=reviewing on a
// running session record. Returns an error if the session isn't running.
func (s *FSStore) SetSessionReviewActive(ctx context.Context, slug, uuid, reviewID string) error {
	rec, err := s.ReadSession(ctx, slug, uuid)
	if err != nil {
		return fmt.Errorf("reading session: %w", err)
	}
	if rec.Status != model.SessionStatusRunning {
		return fmt.Errorf("session %s/%s is %s, not running", slug, uuid, rec.Status)
	}
	rec.ActiveReviewID = reviewID
	rec.Activity = model.SessionActivityReviewing
	return s.WriteSession(ctx, slug, uuid, *rec)
}

// ClearSessionReview clears the review-pending state on a session record:
// ActiveReviewID is emptied and Activity drops back to active (the agent's
// next hook will resync the precise state). No-ops if neither field is set.
func (s *FSStore) ClearSessionReview(ctx context.Context, slug, uuid string) error {
	rec, err := s.ReadSession(ctx, slug, uuid)
	if err != nil {
		return fmt.Errorf("reading session: %w", err)
	}
	if rec.ActiveReviewID == "" && rec.Activity != model.SessionActivityReviewing {
		return nil
	}
	rec.ActiveReviewID = ""
	if rec.Activity == model.SessionActivityReviewing {
		rec.Activity = model.SessionActivityActive
	}
	return s.WriteSession(ctx, slug, uuid, *rec)
}

// FindRunningSession returns the running session record for the given
// (idea, agent type), or nil if none. This is the source of truth for the
// single-session-per-(idea,agent) UX guard. The persisted Status is
// trusted — stale running records (process gone) are reconciled on app
// startup, not at lookup time.
func (s *FSStore) FindRunningSession(ctx context.Context, slug, agentType string) (*model.AgentSession, error) {
	sessions, err := s.ListSessions(ctx, slug)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if sessions[i].Status == model.SessionStatusRunning && sessions[i].Agent == agentType {
			return &sessions[i], nil
		}
	}
	return nil, nil
}

// IdeaSessionSummary holds the per-idea session signal used by the dashboard:
// running session UUIDs by agent, plus the most recent session UUID for
// quick-jump when nothing is running. Cheap to compute in batch — one disk
// scan per idea, surfaced via ListSessionSummaries below.
//
// IdeaSummary is the headless-generated summary sidecar (model.Summary)
// when one exists on disk. The dashboard prefers this over the
// truncated idea body for the card's primary line. Nil when no
// sidecar exists yet (fresh idea, or before the first sweep).
type IdeaSessionSummary struct {
	Slug         string               `json:"slug"`
	RunningCount int                  `json:"runningCount"`
	Running      []model.AgentSession `json:"running,omitempty"`
	// Dormant lists sessions whose Claude process exited cleanly but
	// remain resumable via StartIdeaSession(resume=true). The quick
	// switcher uses this to auto-resume non-terminated sessions
	// instead of routing through the idea page.
	Dormant     []model.AgentSession             `json:"dormant,omitempty"`
	MostRecent  *model.AgentSession              `json:"mostRecent,omitempty"`
	ByAgent     map[string]model.SessionActivity `json:"byAgent,omitempty"`
	IdeaSummary *model.Summary                   `json:"ideaSummary,omitempty"`
	// RepoNames are the short names of linked worktrees under
	// <idea>/repos/, sorted lexicographically. Computed via a
	// directory scan only (no git probing) so the dashboard's
	// batch fetch stays cheap. The full RepoLink shape (branch,
	// dirty, ahead/behind) is reserved for ListRepos, which the
	// idea-detail view fans out to per-idea.
	RepoNames []string `json:"repoNames,omitempty"`
}

// ListSessionSummaries returns a session summary per idea, in the same
// order as List() — sorted by Updated desc. Used by the dashboard to
// render activity dots and quick-jump links without N separate
// ListSessions round-trips from the frontend.
//
// Returns a slice (not a map) so Wails can walk the value type into the
// generated TypeScript bindings — Wails doesn't walk map-value structs.
// The frontend builds the slug → summary lookup.
func (s *FSStore) ListSessionSummaries(ctx context.Context) ([]IdeaSessionSummary, error) {
	ideas, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IdeaSessionSummary, 0, len(ideas))
	for _, idea := range ideas {
		summary := IdeaSessionSummary{Slug: idea.Slug}
		sessions, err := s.ListSessions(ctx, idea.Slug)
		if err != nil {
			// Surface as empty summary; one bad idea shouldn't fail the dashboard.
			out = append(out, summary)
			continue
		}
		// ListSessions returns Started-desc; first non-running is MostRecent.
		var mostRecent *model.AgentSession
		byAgent := make(map[string]model.SessionActivity)
		for i := range sessions {
			switch sessions[i].Status {
			case model.SessionStatusRunning:
				summary.Running = append(summary.Running, sessions[i])
				summary.RunningCount++
				if _, ok := byAgent[sessions[i].Agent]; !ok {
					activity := sessions[i].Activity
					if activity == "" {
						activity = model.SessionActivityIdle
					}
					byAgent[sessions[i].Agent] = activity
				}
			case model.SessionStatusDormant:
				summary.Dormant = append(summary.Dormant, sessions[i])
				if mostRecent == nil {
					mr := sessions[i]
					mostRecent = &mr
				}
			default:
				if mostRecent == nil {
					mr := sessions[i]
					mostRecent = &mr
				}
			}
		}
		summary.MostRecent = mostRecent
		if len(byAgent) > 0 {
			summary.ByAgent = byAgent
		}
		// Best-effort sidecar read. A missing file is the empty
		// state; a parse failure leaves IdeaSummary nil so the card
		// falls back to the truncated body.
		if sum, err := s.ReadSummary(ctx, idea.Slug); err == nil {
			summary.IdeaSummary = sum
		}
		// Cheap repo-name scan: directory entries only, no git
		// status. Names are the worktree dir basenames, which the
		// LinkRepo flow already collapses to the canonical short
		// form (e.g. "ideate", not "github.com/paultyng/ideate").
		summary.RepoNames = listRepoNames(s.ideaDir(idea.Slug))
		out = append(out, summary)
	}
	return out, nil
}

func listRepoNames(ideaDir string) []string {
	entries, err := os.ReadDir(filepath.Join(ideaDir, reposDir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ResolveRepoPath verifies that repoPath refers to a repository configured for
// the idea (either the worktree under <idea>/repos/<name> or the original
// linked path), returning the cleaned absolute path on success. Used at trust
// boundaries (MCP tool args, Wails bindings) to prevent path traversal.
func (s *FSStore) ResolveRepoPath(ctx context.Context, slug, repoPath string) (string, error) {
	if repoPath == "" {
		return "", fmt.Errorf("repo path is required")
	}

	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolving repo path: %w", err)
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	// Worktree under <idea>/repos/<name>.
	reposRoot, err := filepath.Abs(filepath.Join(s.ideaDir(slug), reposDir))
	if err == nil {
		if real, err := filepath.EvalSymlinks(reposRoot); err == nil {
			reposRoot = real
		}
		if rel, err := filepath.Rel(reposRoot, abs); err == nil {
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) == 1 && parts[0] != "." && parts[0] != ".." && !strings.HasPrefix(parts[0], "..") {
				return abs, nil
			}
		}
	}

	// Canonical clone path of any linked worktree (e.g. caller passed the
	// shared canonical repo location rather than the per-idea worktree).
	entries, err := os.ReadDir(filepath.Join(s.ideaDir(slug), reposDir))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wt := filepath.Join(s.ideaDir(slug), reposDir, e.Name())
			canonical, err := repo.Canonical(ctx, wt)
			if err != nil {
				continue
			}
			canonAbs, err := filepath.Abs(canonical)
			if err != nil {
				continue
			}
			if real, err := filepath.EvalSymlinks(canonAbs); err == nil {
				canonAbs = real
			}
			if canonAbs == abs {
				return abs, nil
			}
		}
	}

	return "", fmt.Errorf("repo path %q is not configured for idea %q", repoPath, slug)
}

// ListRepos returns linked repositories for an idea. Path is the worktree
// location relative to the idea root; OriginURL/Branch/Dirty/Ahead/Behind are
// computed live by querying each worktree via git. Broken or non-worktree
// subdirectories yield a record with Name and Path populated only.
func (s *FSStore) ListRepos(ctx context.Context, slug string) ([]RepoLink, error) {
	dir := filepath.Join(s.ideaDir(slug), reposDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading repos dir: %w", err)
	}

	defaultBranch := s.branchPrefixVal() + slug

	var links []RepoLink
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wt := filepath.Join(dir, e.Name())
		rl := RepoLink{
			Name: e.Name(),
			Path: filepath.Join(reposDir, e.Name()),
		}

		if origin, err := repo.OriginURL(ctx, wt); err == nil {
			rl.OriginURL = origin
		}
		if st, err := repo.ReadStatus(ctx, wt); err == nil {
			rl.Branch = st.Branch
			rl.Dirty = st.Dirty
			rl.Ahead = st.Ahead
			rl.Behind = st.Behind
			rl.IsDefaultBranch = st.Branch == defaultBranch
		}
		links = append(links, rl)
	}
	return links, nil
}

// errLifecycleRequiresService is returned by FSStore's lifecycle stubs.
var errLifecycleRequiresService = errors.New("lifecycle methods require *service.IdeaService; FSStore is store-only")

// Archive satisfies mcp.IdeaStore. FSStore does not implement lifecycle ops —
// use *service.IdeaService instead.
func (s *FSStore) Archive(_ context.Context, _ string, _ bool) (*model.ArchiveReport, error) {
	return nil, errLifecycleRequiresService
}

// Unarchive satisfies mcp.IdeaStore. FSStore does not implement lifecycle ops —
// use *service.IdeaService instead.
func (s *FSStore) Unarchive(_ context.Context, _ string) (*model.UnarchiveReport, error) {
	return nil, errLifecycleRequiresService
}

// Pause satisfies mcp.IdeaStore. FSStore does not implement lifecycle ops —
// use *service.IdeaService instead.
func (s *FSStore) Pause(_ context.Context, _ string, _ *time.Time) error {
	return errLifecycleRequiresService
}

// Resume satisfies mcp.IdeaStore. FSStore does not implement lifecycle ops —
// use *service.IdeaService instead.
func (s *FSStore) Resume(_ context.Context, _ string) error {
	return errLifecycleRequiresService
}
