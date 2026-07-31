package summarizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
	ext_store "github.com/paultyng/ideate/internal/store"
)

// fakeStore is an in-memory Store used by the unit tests.
type fakeStore struct {
	mu          sync.Mutex
	ideas       map[string]*model.Idea
	sessions    map[string][]model.AgentSession
	repos       map[string][]ext_store.RepoLink
	written     map[string]model.Summary
	addResource func(slug string, res model.Resource) error // optional override
	addedRes    []addedResource
}

type addedResource struct {
	slug string
	res  model.Resource
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		ideas:    map[string]*model.Idea{},
		sessions: map[string][]model.AgentSession{},
		repos:    map[string][]ext_store.RepoLink{},
		written:  map[string]model.Summary{},
	}
}

func (f *fakeStore) AddResource(_ context.Context, slug string, res model.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedRes = append(f.addedRes, addedResource{slug: slug, res: res})
	if f.addResource != nil {
		return f.addResource(slug, res)
	}
	return nil
}

func (f *fakeStore) Get(_ context.Context, slug string) (*model.Idea, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.ideas[slug]
	if !ok {
		return nil, errors.New("not found")
	}
	return i, nil
}

func (f *fakeStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[slug], nil
}

func (f *fakeStore) ListRepos(_ context.Context, slug string) ([]ext_store.RepoLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[slug], nil
}

func (f *fakeStore) WriteSummary(_ context.Context, slug string, sum model.Summary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written[slug] = sum
	return nil
}

func (f *fakeStore) waitForWrite(t *testing.T, slug string) model.Summary {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		sum, ok := f.written[slug]
		f.mu.Unlock()
		if ok {
			return sum
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("summary never written for %q", slug)
	return model.Summary{}
}

// fakeGenerator is a test-only Generator implementing the
// minimal-fake (not call-counting-mock) pattern from
// testing-philosophy.md. Records every input it received so tests can
// assert on what the Summarizer built.
type fakeGenerator struct {
	mu                 sync.Mutex
	line               string
	suggestedResources []model.Resource
	err                error
	inputs             []GenerateInput
}

func (f *fakeGenerator) Generate(_ context.Context, in GenerateInput) (GenerateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	return GenerateResult{Line: f.line, SuggestedResources: f.suggestedResources}, f.err
}

func (f *fakeGenerator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func (f *fakeGenerator) lastInput() GenerateInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) == 0 {
		return GenerateInput{}
	}
	return f.inputs[len(f.inputs)-1]
}

func TestSummarizer_RegeneratesAndWritesSidecar(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{
		Slug: "foo",
		Name: "Foo",
		Body: "Original body text",
	}
	ended := time.Now().Add(-time.Hour)
	store.sessions["foo"] = []model.AgentSession{{
		UUID:    "11111111-1111-1111-1111-111111111111",
		Status:  model.SessionStatusCompleted,
		Ended:   &ended,
		Started: ended.Add(-time.Minute),
	}}

	gen := &fakeGenerator{line: "Refactoring authentication tokens for new client credentials flow."}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()

	if ok := s.Enqueue("foo"); !ok {
		t.Fatal("Enqueue returned false")
	}
	sum := store.waitForWrite(t, "foo")

	if sum.Line == "" {
		t.Errorf("line is empty")
	}
	if sum.SourceSessionUUID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("source uuid = %q", sum.SourceSessionUUID)
	}
	if sum.SourceSessionEndedAt == nil {
		t.Errorf("source ended_at not set")
	}
	if sum.GeneratedAt.IsZero() {
		t.Errorf("generated_at is zero")
	}
}

// regenerate must populate every new GenerateInput field — status,
// idea-dir, repo paths, vscreen path — when the wiring is set.
// Asserts the bridge between store data + Summarizer state and the
// generator contract is closed end-to-end.
func TestRegenerate_PassesAllPointersToGenerator(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ideasDir := "/ideas"
	store.ideas["repo-idea"] = &model.Idea{
		Slug:   "repo-idea",
		Name:   "Repo Idea",
		Status: model.StatusActive,
		Body:   "Body.",
	}
	store.repos["repo-idea"] = []ext_store.RepoLink{
		{Name: "api", Path: "repos/api"},
		{Name: "web", Path: "repos/web"},
	}
	ended := time.Now().Add(-time.Hour)
	store.sessions["repo-idea"] = []model.AgentSession{{
		UUID:    "sess-uuid-1",
		Status:  model.SessionStatusCompleted,
		Ended:   &ended,
		Started: ended.Add(-time.Minute),
	}}

	gen := &fakeGenerator{line: "Researching pipeline migration."}
	s := New(gen, store, WithIdeasDir(ideasDir))
	s.Start(context.Background(), 1)
	defer s.Stop()
	s.Enqueue("repo-idea")
	store.waitForWrite(t, "repo-idea")

	in := gen.lastInput()
	if in.Status != "active" {
		t.Errorf("Status = %q, want active", in.Status)
	}
	if in.IdeaDir != "/ideas/repo-idea" {
		t.Errorf("IdeaDir = %q, want /ideas/repo-idea", in.IdeaDir)
	}
	if len(in.RepoPaths) != 2 {
		t.Fatalf("RepoPaths len = %d, want 2", len(in.RepoPaths))
	}
	if in.RepoPaths[0] != "/ideas/repo-idea/repos/api" {
		t.Errorf("RepoPaths[0] = %q", in.RepoPaths[0])
	}
	if in.RepoPaths[1] != "/ideas/repo-idea/repos/web" {
		t.Errorf("RepoPaths[1] = %q", in.RepoPaths[1])
	}
	if in.VscreenPath != "/ideas/repo-idea/sessions/sess-uuid-1.vscreen.ansi" {
		t.Errorf("VscreenPath = %q", in.VscreenPath)
	}
	if in.SessionUUID != "sess-uuid-1" {
		t.Errorf("SessionUUID = %q", in.SessionUUID)
	}
}

func TestSummarizer_SkipsRunningSessions(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo", Name: "Foo"}
	store.sessions["foo"] = []model.AgentSession{
		{UUID: "running-uuid", Status: model.SessionStatusRunning, Started: time.Now()},
	}

	gen := &fakeGenerator{line: "Some summary line."}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()
	s.Enqueue("foo")

	sum := store.waitForWrite(t, "foo")
	if sum.SourceSessionUUID != "" {
		t.Errorf("running session leaked into source: %q", sum.SourceSessionUUID)
	}
}

func TestSummarizer_PicksMostRecentlyEnded(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo", Name: "Foo"}
	older := time.Now().Add(-10 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	store.sessions["foo"] = []model.AgentSession{
		{UUID: "older", Status: model.SessionStatusCompleted, Ended: &older},
		{UUID: "newer", Status: model.SessionStatusCompleted, Ended: &newer},
	}

	gen := &fakeGenerator{line: "Line."}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()
	s.Enqueue("foo")

	sum := store.waitForWrite(t, "foo")
	if sum.SourceSessionUUID != "newer" {
		t.Errorf("picked %q, want newer", sum.SourceSessionUUID)
	}
}

func TestSummarizer_CoalescesDuplicates(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo", Name: "Foo"}

	gen := &fakeGenerator{line: "Line."}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()

	// Three rapid enqueues for the same slug — should coalesce.
	s.Enqueue("foo")
	s.Enqueue("foo")
	s.Enqueue("foo")
	store.waitForWrite(t, "foo")

	// Give any leaked duplicate runs a moment to surface.
	time.Sleep(50 * time.Millisecond)
	if got := gen.callCount(); got != 1 {
		t.Errorf("generator invoked %d times, want 1 (dedup)", got)
	}
}

func TestSummarizer_DropsEmptyOutput(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo", Name: "Foo"}

	// ErrEmpty is the conventional "nothing to write" signal. The
	// Summarizer must not write a sidecar in that case.
	gen := &fakeGenerator{err: ErrEmpty}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()
	s.Enqueue("foo")

	time.Sleep(150 * time.Millisecond)
	store.mu.Lock()
	_, written := store.written["foo"]
	store.mu.Unlock()
	if written {
		t.Errorf("expected no summary written when generator returns ErrEmpty")
	}
}

func TestSummarizer_QueueFullReturnsFalse(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo"}
	gen := &fakeGenerator{line: "line"}
	s := New(gen, store)
	// Smaller queue to make exhaustion observable.
	s.queue = make(chan string, 2)
	// Don't start workers — queue won't drain.

	ok1 := s.Enqueue("a")
	ok2 := s.Enqueue("b")
	ok3 := s.Enqueue("c")
	if !ok1 || !ok2 {
		t.Errorf("first two enqueues should succeed: %v %v", ok1, ok2)
	}
	if ok3 {
		t.Errorf("third enqueue should fail (queue full)")
	}
}

// TestEnqueue_SkipsOrchestratorSlug — the synthetic OrchestratorSlug
// has no idea.md and nothing summarizable. Enqueue must filter it
// before it reaches the worker; otherwise the regenerate path emits
// a `loading idea: ... no such file` warn every cycle.
func TestEnqueue_SkipsOrchestratorSlug(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	gen := &fakeGenerator{line: "line"}
	s := New(gen, store)

	if s.Enqueue(model.OrchestratorSlug) {
		t.Errorf("Enqueue(OrchestratorSlug) returned true; should be filtered")
	}
	if s.Enqueue("") {
		t.Errorf("Enqueue(\"\") returned true; should be filtered")
	}

	// And no generator call should have been triggered (since the
	// orchestrator wasn't queued, the worker never picks it up).
	// We don't Start workers here — but just in case, give a brief
	// breath to ensure no goroutine sneaks through.
	time.Sleep(10 * time.Millisecond)
	if gen.callCount() != 0 {
		t.Errorf("generator called %d times for orchestrator slug; want 0", gen.callCount())
	}
}

// TestRegenerate_OrchestratorSlugIsSilentNoOp — defense-in-depth
// guard inside regenerate itself: if any future caller bypasses
// Enqueue (e.g. a direct test or a refactored call path), regenerate
// returns nil silently rather than hitting store.Get and surfacing a
// missing-file error.
func TestRegenerate_OrchestratorSlugIsSilentNoOp(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	// Intentionally do NOT populate store.ideas[OrchestratorSlug] —
	// the guard must short-circuit before store.Get is called.
	gen := &fakeGenerator{}
	s := New(gen, store)

	if err := s.regenerate(context.Background(), model.OrchestratorSlug); err != nil {
		t.Errorf("regenerate(OrchestratorSlug) returned %v; want nil", err)
	}
	if gen.callCount() != 0 {
		t.Errorf("generator called %d times; want 0", gen.callCount())
	}
}

func TestSanitizeSummary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"  hello world  ", "hello world"},
		{`"wrapped in quotes"`, "wrapped in quotes"},
		{"line one\nline two", "line one line two"},
		{"multiple   spaces", "multiple spaces"},
	}
	for _, c := range cases {
		got := sanitizeSummary(c.in)
		if got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSummarizer_TranscriptTailFedToGenerator(t *testing.T) {
	t.Parallel()
	// Write a transcript file under a fake projects dir; assert the
	// generator's input carries the transcript text.
	projectsDir := t.TempDir()
	workingDir := "/some/working/dir"
	encoded := filepath.Join(projectsDir, "-some-working-dir")
	if err := writeTranscriptFor(t, encoded, "sess-uuid"); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	store := newFakeStore()
	store.ideas["foo"] = &model.Idea{Slug: "foo", Name: "Foo"}
	ended := time.Now().Add(-time.Hour)
	store.sessions["foo"] = []model.AgentSession{{
		UUID:       "sess-uuid",
		Status:     model.SessionStatusCompleted,
		Ended:      &ended,
		WorkingDir: workingDir,
	}}

	gen := &fakeGenerator{line: "Refactoring auth tokens."}
	s := New(gen, store, WithProjectsDir(projectsDir))
	s.Start(context.Background(), 1)
	defer s.Stop()
	s.Enqueue("foo")
	store.waitForWrite(t, "foo")

	in := gen.lastInput()
	if in.IdeaName != "Foo" {
		t.Errorf("input.IdeaName = %q", in.IdeaName)
	}
	if in.SessionUUID != "sess-uuid" {
		t.Errorf("input.SessionUUID = %q", in.SessionUUID)
	}
	for _, f := range []string{"real question", "real answer"} {
		if !strings.Contains(in.TranscriptTail, f) {
			t.Errorf("transcript tail missing %q\ntail:\n%s", f, in.TranscriptTail)
		}
	}
}

func writeTranscriptFor(t *testing.T, dir, sessionUUID string) error {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := `{"type":"user","message":{"role":"user","content":"real question"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"real answer"}]}}
`
	return os.WriteFile(filepath.Join(dir, sessionUUID+".jsonl"), []byte(content), 0o644)
}

// TestRegenerate_AppliesSuggestedResources verifies that resources
// returned by Generator.Generate land on the store via AddResource
// after the summary sidecar is written.
func TestRegenerate_AppliesSuggestedResources(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["bar"] = &model.Idea{Slug: "bar", Name: "Bar"}
	ended := time.Now().Add(-time.Hour)
	store.sessions["bar"] = []model.AgentSession{{
		UUID:    "22222222-2222-2222-2222-222222222222",
		Status:  model.SessionStatusCompleted,
		Ended:   &ended,
		Started: ended.Add(-time.Minute),
	}}

	gen := &fakeGenerator{
		line: "Wired up the new auth flow.",
		suggestedResources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/o/r/pull/1", Label: "Auth PR"},
			{Type: "notion", URL: "https://notion.so/p1", Label: "Design doc"},
		},
	}
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()

	if ok := s.Enqueue("bar"); !ok {
		t.Fatal("Enqueue returned false")
	}
	_ = store.waitForWrite(t, "bar")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addedRes) != 2 {
		t.Fatalf("AddResource called %d times, want 2; got %+v", len(store.addedRes), store.addedRes)
	}
	for i, want := range gen.suggestedResources {
		got := store.addedRes[i]
		if got.slug != "bar" {
			t.Errorf("call %d slug = %q, want %q", i, got.slug, "bar")
		}
		if got.res.URL != want.URL || got.res.Type != want.Type || got.res.Label != want.Label {
			t.Errorf("call %d res = %+v, want %+v", i, got.res, want)
		}
	}
}

// TestRegenerate_NoSuggestedResources_NoCalls confirms an empty
// suggested_resources slice produces zero AddResource calls.
func TestRegenerate_NoSuggestedResources_NoCalls(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ideas["empty"] = &model.Idea{Slug: "empty", Name: "Empty"}
	ended := time.Now().Add(-time.Hour)
	store.sessions["empty"] = []model.AgentSession{{
		UUID:    "33333333-3333-3333-3333-333333333333",
		Status:  model.SessionStatusCompleted,
		Ended:   &ended,
		Started: ended.Add(-time.Minute),
	}}

	gen := &fakeGenerator{line: "Did things."} // no suggestedResources
	s := New(gen, store)
	s.Start(context.Background(), 1)
	defer s.Stop()

	if ok := s.Enqueue("empty"); !ok {
		t.Fatal("Enqueue returned false")
	}
	_ = store.waitForWrite(t, "empty")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addedRes) != 0 {
		t.Errorf("AddResource called %d times, want 0; got %+v", len(store.addedRes), store.addedRes)
	}
}
