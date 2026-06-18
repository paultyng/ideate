// Package claudecode provides typed structures and helpers for integrating
// with the Claude Code CLI — settings files, MCP configuration, hook
// definitions, and CLI command building.
package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paultyng/ideate/internal/bindisco"
	"github.com/paultyng/ideate/internal/model"
)

// claudeNotFoundHint is appended to the discovery-failure error so users
// see both escape hatches inline. Listed in priority order: env var wins
// because it's per-launch and doesn't require editing config.json.
const claudeNotFoundHint = `Two ways to fix:
  1. Set IDEATE_CLAUDE_BINARY in your environment and relaunch Ideate:
       export IDEATE_CLAUDE_BINARY=/absolute/path/to/claude
  2. Add agents.claude.binary to <ideas-dir>/config.json:
       { "agents": { "claude": { "binary": "/absolute/path/to/claude" } } }

To find the absolute path, run this from a terminal:
  which claude`

// claudeExtraCommonPaths returns claude-specific install locations that
// bindisco's per-OS curated list doesn't carry. Today: the Anthropic
// installer's ~/.claude/local/claude. Skipped on home-dir resolution
// failure (the rare /etc/passwd-misconfigured case) so a missing HOME
// just degrades to the standard tiers rather than panicking.
func claudeExtraCommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".claude", "local", "claude")}
}

// SessionHeader is the HTTP header used to identify the agent session.
// Used by MCP requests, where each Claude process registers a per-session
// MCP server keyed by this id.
const SessionHeader = "X-Ideate-Session-Id"

// IdeaSlugHeader is the HTTP header that carries the idea slug on hook
// POSTs. Hook resolution is slug-based (vs MCP's session-id-based) because
// `/clear` and `/compact` change Claude's internal session id mid-process,
// while the slug stays constant for the Claude process's lifetime — so the
// running session can be found via store.FindRunningSession(slug, agent)
// without coordinator-side session-id bookkeeping.
const IdeaSlugHeader = "X-Ideate-Idea-Slug"

// Settings represents a Claude Code settings.json file.
type Settings struct {
	Hooks       map[string][]HookMatcher `json:"hooks,omitempty"`
	Permissions *Permissions             `json:"permissions,omitempty"`
}

// HookMatcher pairs a file-glob matcher with the hooks to run when it matches.
type HookMatcher struct {
	Matcher string `json:"matcher"`
	Hooks   []Hook `json:"hooks"`
}

// Hook is a single hook definition inside a matcher.
type Hook struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Timeout int               `json:"timeout"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Permissions controls which tools/actions are auto-allowed.
type Permissions struct {
	Allow []string `json:"allow,omitempty"`
}

// MCPConfig represents a Claude Code --mcp-config file.
type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// MCPServer is a single MCP server entry in the config.
type MCPServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Option configures config file generation.
type Option func(*options)

type options struct {
	headers map[string]string
}

// WithHeader adds a custom HTTP header to generated hook and MCP configs.
func WithHeader(key, value string) Option {
	return func(o *options) {
		if o.headers == nil {
			o.headers = make(map[string]string)
		}
		o.headers[key] = value
	}
}

func resolveOpts(opts []Option) options {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// GenerateSettingsFile writes a temp Claude Code settings file with hook URLs
// and MCP permissions. hooksURL is the base endpoint (e.g. http://localhost:PORT/hooks).
func GenerateSettingsFile(hooksURL string, opts ...Option) (string, error) {
	o := resolveOpts(opts)

	mkHook := func(path string, timeout int) Hook {
		h := Hook{Type: "http", URL: hooksURL + "/" + path, Timeout: timeout}
		if len(o.headers) > 0 {
			h.Headers = copyHeaders(o.headers)
		}
		return h
	}

	// SessionStart is intentionally NOT subscribed: Claude Code v2.x logs
	// "HTTP hooks are not supported for SessionStart" and silently skips
	// HTTP-type entries (then falls into a "ToolUseContext is required
	// for prompt hooks" bug for any synthesized prompt-type fallback).
	// Sibling-record creation on /clear or /compact lives in HandleEnd
	// instead, keyed off SessionEnd's `reason` field which fires reliably.
	settings := Settings{
		Hooks: map[string][]HookMatcher{
			"Stop":             {{Hooks: []Hook{mkHook("stop", 5)}}},
			"PreToolUse":       {{Hooks: []Hook{mkHook("pre-tool-use", 5)}}},
			"PostToolUse":      {{Hooks: []Hook{mkHook("tool-use", 5)}}},
			"SessionEnd":       {{Hooks: []Hook{mkHook("end", 10)}}},
			"UserPromptSubmit": {{Hooks: []Hook{mkHook("prompt", 5)}}},
			"Notification":     {{Hooks: []Hook{mkHook("notification", 5)}}},
			// PreCompact fires before /compact summarizes the conversation
			// — the only signal we get during the compaction window.
			// SessionEnd reason=compact only fires after the summary
			// completes, so without PreCompact the UI would render idle
			// throughout the actual work.
			"PreCompact": {{Hooks: []Hook{mkHook("pre-compact", 5)}}},
		},
		// Enumerate the auto-approved Ideate tool prefixes rather than
		// blanket-allowing `mcp__ideate__.*`. Destructive tools that
		// touch user data (currently `reset_default_skill`) fall through
		// this list and trigger the harness's per-tool approval prompt,
		// which is the actual user-confirmation surface — the tool
		// itself can't safely gate on a `confirm` arg because the agent
		// can satisfy that arg without user involvement.
		Permissions: &Permissions{
			Allow: []string{
				"mcp__ideate__list_*",
				"mcp__ideate__get_*",
				"mcp__ideate__add_*",
				"mcp__ideate__update_*",
				"mcp__ideate__create_*",
				"mcp__ideate__rename_*",
				"mcp__ideate__delete_*",
				"mcp__ideate__link_*",
				"mcp__ideate__unlink_*",
				"mcp__ideate__request_*",
				"mcp__ideate__cancel_*",
				"mcp__ideate__reply_*",
				"mcp__ideate__send_*",
				"mcp__ideate__goto_*",
				"mcp__ideate__set_*",
				"mcp__ideate__start_*",
			},
		},
	}

	return writeTempJSON("ideate-settings-*.json", settings)
}

// GenerateMCPConfigFile writes a temp MCP config pointing to the Ideate MCP
// server. mcpURL is the full endpoint (e.g. http://localhost:PORT/mcp).
func GenerateMCPConfigFile(mcpURL string, opts ...Option) (string, error) {
	o := resolveOpts(opts)

	srv := MCPServer{Type: "http", URL: mcpURL}
	if len(o.headers) > 0 {
		srv.Headers = copyHeaders(o.headers)
	}

	config := MCPConfig{
		MCPServers: map[string]MCPServer{"ideate": srv},
	}

	return writeTempJSON("ideate-mcp-*.json", config)
}

// CommandConfig holds parameters for building a claude CLI command.
type CommandConfig struct {
	Name         string   // -n flag
	AgentUUID    string   // --session-id for new sessions
	ResumeUUID   string   // --resume for existing sessions
	HooksURL     string   // base URL for hooks (empty = no hooks)
	MCPURL       string   // URL for MCP server (empty = no MCP)
	SessionID    string   // X-Ideate-Session-Id header (MCP)
	IdeaSlug     string   // X-Ideate-Idea-Slug header (hooks; survives /clear)
	SystemPrompt string   // --append-system-prompt content
	AddDirs      []string // --add-dir entries
	Debug        bool     // adds --debug to the claude invocation
	// ExtraArgs is appended verbatim after every Ideate-managed flag.
	// Last-occurrence-wins on the claude CLI means the user can
	// override anything earlier; overriding --settings, --mcp-config,
	// --resume, or --session-id will break Ideate's integration.
	ExtraArgs []string
	// Env vars merged into the spawned process environment. Applied at
	// PTY spawn time in claude.go, not here in BuildCommand. Stored on
	// CommandConfig so the merged set travels with the command description
	// if callers need to inspect it; BuildCommand itself does not consume it.
	Env map[string]string
	// BinaryPath is passed through to bindisco.Resolve as its Override
	// tier — non-empty values are used verbatim, skipping $PATH lookup
	// and the curated-paths fallback. Production callers populate this
	// from the IDEATE_CLAUDE_BINARY env var or the agents.claude.binary
	// config field, in that order. Tests set it to a sentinel so the
	// resolve step doesn't depend on the CI runner having a real claude
	// installed.
	BinaryPath string
}

// BuildCommand finds the claude CLI and returns an exec.Cmd with all arguments
// and a list of temp files to clean up on exit.
//
// SECURITY: bindisco.Resolve (and exec.LookPath underneath) honors the
// user's $PATH. A malicious binary earlier in PATH (compromised npm install
// in node_modules/.bin, modified shell rc) would be spawned with full session
// privileges. Accepted for the v0.1 single-user, local-desktop threat model
// — every component the user runs already has the same trust level. Revisit
// if Ideate ever sandboxes agents, runs as a service, or supports multi-user.
func BuildCommand(config CommandConfig) (*exec.Cmd, []string, error) {
	claudePath, err := bindisco.Resolve("claude", bindisco.Locations{
		Override:         config.BinaryPath,
		ExtraCommonPaths: claudeExtraCommonPaths(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w\n\n%s", err, claudeNotFoundHint)
	}

	args := []string{"-n", config.Name}
	var tempFiles []string

	if config.ResumeUUID != "" {
		args = append(args, "--resume", config.ResumeUUID)
	} else if config.AgentUUID != "" {
		args = append(args, "--session-id", config.AgentUUID)
	}

	// Both MCP and hooks receive the X-Ideate-Session-Id header so the
	// server can authenticate the caller against the live session registry.
	// Hooks also receive X-Ideate-Idea-Slug as a routing hint (stable
	// across Claude's internal /clear and /compact; the session UUID is
	// Ideate's own stable handle, so both are usable). Setting both lets
	// the hook handler validate auth via the session UUID while keeping
	// slug-based routing for /clear-bridging logic.
	var hooksOpts []Option
	if config.SessionID != "" {
		hooksOpts = append(hooksOpts, WithHeader(SessionHeader, config.SessionID))
	}
	if config.IdeaSlug != "" {
		hooksOpts = append(hooksOpts, WithHeader(IdeaSlugHeader, config.IdeaSlug))
	} else {
		// Orchestrator: send the synthetic slug so the handler can resolve
		// the orchestrator's session record (persisted under
		// <ideasDir>/<OrchestratorSlug>/...).
		hooksOpts = append(hooksOpts, WithHeader(IdeaSlugHeader, model.OrchestratorSlug))
	}
	var mcpOpts []Option
	if config.SessionID != "" {
		mcpOpts = append(mcpOpts, WithHeader(SessionHeader, config.SessionID))
	}

	if config.HooksURL != "" {
		path, err := GenerateSettingsFile(config.HooksURL, hooksOpts...)
		if err != nil {
			return nil, nil, fmt.Errorf("generating settings: %w", err)
		}
		tempFiles = append(tempFiles, path)
		args = append(args, "--settings", path)
	}

	if config.MCPURL != "" {
		path, err := GenerateMCPConfigFile(config.MCPURL, mcpOpts...)
		if err != nil {
			cleanupFiles(tempFiles)
			return nil, nil, fmt.Errorf("generating MCP config: %w", err)
		}
		tempFiles = append(tempFiles, path)
		args = append(args, "--mcp-config", path)
	}

	if config.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", config.SystemPrompt)
	}

	for _, dir := range config.AddDirs {
		args = append(args, "--add-dir", dir)
	}

	if config.Debug {
		args = append(args, "--debug")
	}

	// User-configured extras land last so they can override any earlier
	// flag the user controls. Singular Ideate-managed flags are reserved —
	// overriding them silently breaks hooks, MCP, or resume identity, so
	// reject and fail loud. Multi-value flags like --add-dir compose
	// freely and pass through.
	if err := validateExtraArgs(config.ExtraArgs); err != nil {
		return nil, tempFiles, err
	}
	args = append(args, config.ExtraArgs...)

	cmd := exec.Command(claudePath, args...)
	return cmd, tempFiles, nil
}

// reservedExtraArgFlags are singular flags Ideate manages itself. Setting
// any of these via agents.claude.extra_args would replace Ideate's value
// (last occurrence wins) and break hooks / MCP / resume / identity.
// Multi-value flags (--add-dir, --allowedTools) are intentionally omitted
// — they compose with Ideate's values, no collision.
var reservedExtraArgFlags = []string{
	"--settings",
	"--mcp-config",
	"--resume",
	"--session-id",
	"--append-system-prompt",
}

// validateExtraArgs returns a non-nil error if any reserved Ideate-managed
// flag appears in the user-supplied extras. Matches both `--flag value` and
// `--flag=value` shapes.
func validateExtraArgs(extra []string) error {
	for _, a := range extra {
		flag := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			flag = a[:i]
		}
		for _, r := range reservedExtraArgFlags {
			if flag == r {
				return fmt.Errorf("extra_args includes %q, which is reserved for Ideate; remove it from agents.claude.extra_args in config.json", r)
			}
		}
	}
	return nil
}

// BuildOrchestratorSystemPrompt produces the system-prompt appendix for the
// root orchestrator session — an agent that isn't bound to any idea but can
// create / read / update ideas across the workspace via the cross-idea MCP
// tools.
func BuildOrchestratorSystemPrompt() string {
	var b strings.Builder
	b.WriteString("You are running as the Ideate orchestrator: a workspace-wide session that is NOT attached to any single idea.\n")
	b.WriteString("The current working directory is the user's ideas root; each subdirectory is a separate idea.\n\n")
	b.WriteString("Available MCP tools (every per-idea call requires an explicit slug):\n")
	b.WriteString("- list_ideas — enumerate slug, name, status of all ideas\n")
	b.WriteString("- create_idea(name, status?, summary?) — returns the new slug\n")
	b.WriteString("- get_idea_by_slug(slug) — full metadata + resources\n")
	b.WriteString("- update_idea_by_slug(slug, name?, status?, summary?)\n")
	b.WriteString("- add_resource_by_slug(slug, type, url?, label?)\n")
	b.WriteString("- list_resources_by_slug(slug)\n")
	b.WriteString("- list_backlog_by_slug(slug) / add_backlog_item_by_slug(slug, title, body?, status?) / update_backlog_item_by_slug(slug, id, ...) / delete_backlog_item_by_slug(slug, id) — each idea owns a task list\n\n")
	b.WriteString("Use list_ideas first when the user references an idea by name or topic; pick the matching slug before calling per-idea tools.\n\n")
	b.WriteString("send_session_input is fire-and-forget by default — the receiving session runs without routing anything back. Do NOT relay a session's free-form output back to the user or pull them into follow-up questions on the session's behalf; let them navigate into the session themselves to drive it. Only set `include_reply_hint=true` when you genuinely want a structured reply via reply_to_orchestrator (interactive orchestration), and even then, surface the reply briefly rather than re-asking the user every question the session emits.\n\n")
	b.WriteString("Backlog discipline: when the user surfaces work that belongs to an idea (\"we should write a regression test for X\", \"file a follow-up on Y\"), add_backlog_item_by_slug on the relevant idea rather than dropping the work into the current conversation. The backlog is the user's durable cross-session memory and your handoff channel to future idea sessions.")
	return b.String()
}

// BuildSystemPrompt creates the context appendix injected via --append-system-prompt.
//
// The appendix carries idea identity + a file-scope preference. Mutable
// data (resources, repos, branch) and tool catalogs are intentionally
// omitted — sessions always run at the idea root and the agent fetches
// state via list_resources / list_repos / git on demand.
//
// A short review-workflow pointer is appended so the model knows to
// reach for the request_*_review MCP tools when the user asks for a
// review. The tool descriptions themselves carry the workflow detail.
func BuildSystemPrompt(idea model.Idea) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are working on the idea: %s\n", idea.Name)
	fmt.Fprintf(&b, "Status: %s\n", idea.Status)

	b.WriteString(`
<file-scope>
Prefer paths in this order, unless the user explicitly directs you elsewhere:
1. The idea root (the current working directory) — context, notes, history, and per-idea files.
2. Linked repo worktrees under repos/<name>/ — code changes happen here, on the per-idea branch.
3. Other paths only when the user names them.

If you need to work in a repo's files, use link_repo to create a per-idea worktree.
Do not navigate to a canonical clone elsewhere on disk — that loses the per-idea
branch and bypasses the worktree boundary.
</file-scope>`)

	b.WriteString(`

<reviews>
When the user asks to review code changes ("review my diff", "look at the
changes", etc.) call request_diff_review with the repo + base/head refs,
then poll get_diff_review_result. For prose ("review this plan", "look at
the doc") call request_markdown_review with the file path, then poll
get_markdown_review_result. Each get_* call long-polls up to 60s for the
human's submit — call again immediately on "pending"; do not sleep. Use
cancel_review to abandon a pending review. The result of a markdown
review is HUMAN INPUT (CriticMarkup-annotated edits) — see
get_markdown_review_result's tool description for how to apply marks and
direct prose edits to produce the next version of the file.
</reviews>`)

	// NOTE: Multi-agent surface — this <resources> block is Claude-only because
	// BuildSystemPrompt is hand-written. When Cursor / Codex runners ship,
	// they get the same block templated in.
	b.WriteString(`

<resources>
PROACTIVELY track external artifacts as resources on this idea using
add_resource — do not wait for the user to ask. Add a resource the moment
you:
- create or comment on a GitHub PR
- reference a Jira ticket
- create, read, or comment on a Notion doc
- fetch a vendor doc or dashboard URL via WebFetch
- link a repo (link_repo auto-tracks the origin; manual repos still need add_resource)
- touch a feature flag, deploy job, monitor, or other tracked artifact

add_resource is idempotent — re-adding the same URL updates label/status/type
in place (canonical-URL dedupe). See add_resource's tool description for the
full type vocabulary and the type-promotion rule.
</resources>`)

	b.WriteString(`

<backlog>
The idea has a backlog — a durable task list owned by the idea (not by
this session, not by a repo). Use it as your cross-session memory:
without it, follow-ups die at session end.

PROACTIVELY add via add_backlog_items — do not wait for the user to ask. The tool takes an array; pass a single-element array for the trivial case, or stack multiple items into one call when you're filing a related set:
- mid-flow follow-up the user hasn't asked for yet ("we should write a regression test for this", "the doc needs a rollback section")
- work the user explicitly defers ("park that for later")
- blocker discovered mid-session ("need approval from X before continuing")
- a task that belongs on a different idea — use add_backlog_items_by_slug to drop it on that idea, do not orchestrate through the user
- importing a stack of follow-ups from a plan / meeting notes / another tool — one bulk call, not N singular calls

When you know the scope of an item, capture it for the next agent:
- depends_on: list the backlog IDs this item blocks on. Bare "id" for same-idea; "slug:id" for a task on a sibling idea. The agent picking up work next reads depends_on to walk the unblocked subset.
- affects: list the file paths the task is expected to touch (relative to the idea root). The orchestrator partitions open work into non-overlapping affects sets so multiple subagents can run in parallel without write conflicts. Capture this even on items you'll do yourself — it's the parallelization signal for whatever follows.

UPDATE status with update_backlog_items as work progresses (one patch per id; the tool sweeps the whole batch in one call):
- start: status="in_progress"
- finish: status="done"
- abandon: status="wontfix" and put the reason in body so a future agent doesn't re-take it
- a dependency just landed: edit depends_on to drop the resolved id
- scope grew: extend affects so downstream agents know the full file footprint

SURFACE the backlog to the user at natural break points:
- start of a session: list_backlog with status=["open", "in_progress"] to triage active items, ask which to tackle
- before exiting: confirm done items, surface anything still in_progress
- when stuck or finished what was asked: list_backlog with status=["open", "in_progress"] to seed the "what now?" conversation

list_backlog drops each item's body by default to keep responses small. Pass include_body=true when you actually need the body — picking up a long task, summarizing context, or reasoning about scope. Default-off is the right call for triage and status surveys.

Backlog is distinct from GitHub Issues / Jira: items stay inside the
idea, work even with no linked repo, and travel with the idea
directory. Use external trackers when the work needs cross-team
visibility; use the backlog when it's your idea's internal task list.
</backlog>`)

	return b.String()
}

func writeTempJSON(pattern string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling JSON: %w", err)
	}

	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return f.Name(), nil
}

func copyHeaders(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cleanupFiles(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}
