package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/paultyng/ideate/internal/atomicfile"
)

// Config holds ideas-directory-level configuration.
type Config struct {
	BranchPrefix string `json:"branch_prefix,omitempty"`
	// TrackingBranch is the upstream ref configured on each idea's slug-named
	// worktree branch (e.g. "origin/main", "upstream/master"). Defaults to
	// "origin/main" when empty. Feature branches users create off the idea
	// branch get their own upstreams naturally when pushed.
	TrackingBranch string `json:"tracking_branch,omitempty"`

	// Summary configures the idea-summarization backend. Empty / unset
	// defaults to deterministic snippet synthesis; opt up to a headless
	// LLM via Summary.Backend = "claude" / "codex" (etc).
	Summary SummaryConfig `json:"summary,omitempty"`

	// Agents groups per-agent runner settings. Each agent type has
	// its own sub-block (no shared cross-runner fields) since we
	// can't assume future runners share Claude's flag surface.
	Agents AgentsConfig `json:"agents,omitempty"`
}

// AgentsConfig namespaces per-agent runner settings. Future Cursor
// / Codex sub-blocks land alongside Claude here.
type AgentsConfig struct {
	Claude ClaudeAgent `json:"claude,omitempty"`
}

// ClaudeAgent carries user-configured knobs for the Claude runner.
type ClaudeAgent struct {
	// Extra --add-dir paths layered on top of the per-idea AddDirs
	// (idea root + each linked repo path). `~` and `$VARS` are
	// expanded at Run() time so the same config.json travels across
	// machines.
	AddDirs []string `json:"add_dirs,omitempty"`

	// Verbatim Claude flags appended after every Ideate-managed flag.
	// Use cases: --dangerously-skip-permissions, --model, etc.
	// Overriding Ideate's own flags (--settings, --mcp-config,
	// --resume, --session-id) will break hooks / MCP / resume.
	ExtraArgs []string `json:"extra_args,omitempty"`

	// Env vars layered on top of the inherited environment for every
	// Claude spawn. Last-occurrence semantics on the env slice mean
	// these override anything inherited from the parent process.
	Env map[string]string `json:"env,omitempty"`
}

// SummaryConfig picks which Generator the App wires into the
// idea-summary pipeline.
//
// Backends:
//   - ""        → snippet (default; deterministic local, no subprocess).
//   - "snippet" → same as above, explicit.
//   - "claude"  → headless claude --print --output-format stream-json.
//   - "codex"   → headless codex exec --json (not yet implemented;
//     falls back to snippet with a warning).
//   - "testagent" → dev-only; headless testagent claude --print. Only
//     available in builds with the `dev` build tag.
type SummaryConfig struct {
	Backend string `json:"backend,omitempty"`
}

const configFilename = "config.json"

// LoadConfig reads config.json from the ideas directory root.
// Returns defaults if the file does not exist.
func LoadConfig(ideasDir string) (*Config, error) {
	p := filepath.Join(ideasDir, configFilename)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// SaveConfig writes config.json to the ideas directory root.
func SaveConfig(ideasDir string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	data = append(data, '\n')

	p := filepath.Join(ideasDir, configFilename)
	if err := atomicfile.Write(p, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}
