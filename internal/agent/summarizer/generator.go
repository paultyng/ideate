package summarizer

import (
	"context"
	"errors"

	"github.com/paultyng/ideate/internal/model"
)

// GenerateResult is returned by every Generator. Line carries the
// one-sentence summary. SuggestedResources is an optional list of
// resources the generator discovered in the transcript; the
// Summarizer applies them via Store.AddResource after writing the
// summary sidecar. Empty / nil slice is a clean no-op.
type GenerateResult struct {
	Line               string
	SuggestedResources []model.Resource
}

// Generator produces a single-sentence summary line for an idea.
// The Summarizer calls Generate with the idea metadata + transcript
// tail; implementations may run a headless LLM, synthesize the line
// deterministically from the input, or compose other strategies.
//
// Backends are configurable via <ideas-dir>/config.json's
// summary.backend field. Default is the deterministic SnippetGenerator;
// users opt up to a [HeadlessGenerator] (Claude, Codex, or the
// dev-only testagent runner) when they want richer output and are
// OK with the per-SessionEnd Haiku-class cost.
type Generator interface {
	Generate(ctx context.Context, in GenerateInput) (GenerateResult, error)
}

// GenerateInput is the structured input every Generator receives.
// The Summarizer builds it from the idea record + the latest ended
// session's transcript tail. None of the fields are guaranteed
// non-empty:
//
//   - For a fresh idea with no sessions, TranscriptTail is "".
//   - For an idea whose latest session has no assistant text reply
//     (turn died mid-thinking, ToolUse-only turn), TranscriptTail may
//     be "" even though SessionUUID is set.
//
// Generators decide their own fallbacks; the Summarizer just writes
// the returned line.
type GenerateInput struct {
	IdeaName       string
	IdeaBody       string // raw body of idea.md (post-frontmatter)
	TranscriptTail string // last N user+assistant turns, "" if no session
	SessionUUID    string // latest ended session's UUID, "" if none

	// Lifecycle anchors. Empty values render absent in the prompt;
	// the snippet generator ignores them so they're additive.
	Status string // model.Status: active|paused|archived
	Phase  string // freeform string per model.Idea
	Type   string // model.IdeaType: feature|bug|incident|research|review

	// Absolute filesystem pointers the headless agent can drive
	// tools (Bash for git, Read for transcripts/vscreen) against.
	// Each may be empty; the prompt template skips empty entries.
	IdeaDir        string   // <ideasDir>/<slug>
	RepoPaths      []string // each linked worktree's absolute path
	TranscriptPath string   // <claudeProjectsDir>/<encoded-cwd>/<uuid>.jsonl
	VscreenPath    string   // <ideaDir>/sessions/<uuid>.vscreen.ansi
}

// ErrEmpty signals "no meaningful summary available" — the Summarizer
// treats this as a no-op (no sidecar written) so the dashboard falls
// back to its truncated-body rendering. Distinct from a Generate
// error: ErrEmpty is normal, errors are logged.
var ErrEmpty = errors.New("summarizer: empty generator output")
