package summarizer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/paultyng/ideate/internal/agent/headless"
	"github.com/paultyng/ideate/internal/model"
)

// HeadlessGenerator wraps a [headless.Runner] (Claude, Codex, the
// dev-only testagent runner, …) and produces the summary line by
// running an LLM prompt. The cost lever — every Generate call spawns
// a subprocess and burns Haiku-class tokens — is the user's reason
// to opt in via the summary.backend config field.
type HeadlessGenerator struct {
	Runner       headless.Runner
	Model        string // e.g. "haiku"; empty = upstream default
	SystemPrompt string // overrides defaultSystemPrompt when set

	// Logger receives `summarizer.warning` (one per model-emitted
	// warning) and `summarizer.parse_fallback` (when the model
	// ignores the JSON schema). nil = slog.Default(). The warnings
	// channel is debug-only — it never surfaces to the user.
	Logger *slog.Logger
}

// summaryWire is the wire shape the model is asked to emit. Extra
// fields are ignored on decode; missing `warnings` is treated as the
// empty slice. Missing `suggested_resources` is a clean no-op.
type summaryWire struct {
	Summary            string           `json:"summary"`
	Warnings           []string         `json:"warnings,omitempty"`
	SuggestedResources []model.Resource `json:"suggested_resources,omitempty"`
}

// parseFallbackPreviewLen caps how much of the malformed reply lands
// in the parse_fallback log line. 200 chars is enough to diagnose
// schema drift without flooding the log on a runaway response.
const parseFallbackPreviewLen = 200

// Generate implements [Generator]. Builds the same prompt the
// pre-refactor Summarizer built; this is just an interface-shaped
// surface for the existing logic.
func (g HeadlessGenerator) Generate(ctx context.Context, in GenerateInput) (GenerateResult, error) {
	if g.Runner == nil {
		return GenerateResult{}, fmt.Errorf("headless generator: runner is nil")
	}
	prompt := buildHeadlessPrompt(in)
	systemPrompt := g.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}
	rc, err := g.Runner.Run(ctx, prompt, headless.Opts{
		Model:        g.Model,
		SystemPrompt: systemPrompt,
		WorkingDir:   in.IdeaDir,
	})
	if err != nil {
		return GenerateResult{}, fmt.Errorf("headless run: %w", err)
	}
	line, err := headless.DrainText(ctx, rc)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("headless drain: %w", err)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return GenerateResult{}, ErrEmpty
	}

	wire, ok := parseSummaryReply(trimmed)
	logger := g.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if !ok {
		preview := trimmed
		if len(preview) > parseFallbackPreviewLen {
			preview = preview[:parseFallbackPreviewLen]
		}
		logger.Info("summarizer.parse_fallback",
			slog.String("session_uuid", in.SessionUUID),
			slog.String("reply_preview", preview))
		// Treat the raw reply as the summary line so a model that
		// ignored the schema still produces something usable.
		return GenerateResult{Line: trimmed}, nil
	}
	for _, w := range wire.Warnings {
		logger.Info("summarizer.warning",
			slog.String("session_uuid", in.SessionUUID),
			slog.String("text", w))
	}
	if strings.TrimSpace(wire.Summary) == "" {
		return GenerateResult{}, ErrEmpty
	}
	return GenerateResult{
		Line:               wire.Summary,
		SuggestedResources: wire.SuggestedResources,
	}, nil
}

// parseSummaryReply attempts to decode the model's reply as the
// {summary, warnings, suggested_resources} JSON object. Returns
// (wire, true) on a clean parse, ({}, false) when the reply isn't
// JSON at all or the required `summary` field is absent. Lenient on
// extra fields.
//
// Resilient to two real-world drifts Claude exhibits in
// summary.backend=claude calls despite the prompt's "no markdown
// fence" directive:
//
//  1. Wrapping the object in a ```json ... ``` fence.
//  2. Prefixing with a sentence of prose ("I'll read the transcript...").
//
// We strip both before parsing.
func parseSummaryReply(reply string) (summaryWire, bool) {
	body := extractJSONObject(reply)
	if body == "" {
		return summaryWire{}, false
	}
	var w summaryWire
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		return summaryWire{}, false
	}
	if w.Summary == "" {
		return summaryWire{}, false
	}
	return w, true
}

// extractJSONObject pulls the first balanced `{...}` substring out of
// reply, peeling away any leading prose and any surrounding markdown
// code fence. Returns "" when no candidate is found. The brace count
// only respects characters outside of JSON string literals so embedded
// `{` / `}` inside string values don't confuse the scan.
func extractJSONObject(reply string) string {
	// Strip a leading ```...\n and trailing ``` fence, language tag
	// optional. Cheap pre-pass before the brace scan.
	trimmed := strings.TrimSpace(reply)
	if strings.HasPrefix(trimmed, "```") {
		// Drop the first fence-and-language line.
		if nl := strings.Index(trimmed, "\n"); nl >= 0 {
			trimmed = trimmed[nl+1:]
		} else {
			trimmed = strings.TrimPrefix(trimmed, "```")
		}
		// Drop a trailing fence if present.
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}

	start := strings.IndexByte(trimmed, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(trimmed); i++ {
		c := trimmed[i]
		if inStr {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return trimmed[start : i+1]
			}
		}
	}
	return ""
}

// buildHeadlessPrompt is the LLM-facing prompt template. Lives here
// rather than on the Summarizer so HeadlessGenerator is the single
// owner of "what we ask the LLM" for idea summaries.
//
// Shape:
//   - <task> defines the deliverable + the JSON output contract.
//   - <idea name="..." type="..." status="..." phase="..."> wraps the
//     user's first-person body with an explicit voice annotation so
//     the model rewrites rather than parrots.
//   - <context> names absolute paths the agent can `Read` / `git log`
//     selectively (idea root, each linked repo, transcript JSONL,
//     vscreen snapshot). Empty fields are omitted.
//   - <research-hints> nudges the agent toward useful tool calls.
//   - <transcript-tail> is the cheap inline fallback when the model
//     skips the file-based research path.
func buildHeadlessPrompt(in GenerateInput) string {
	var b strings.Builder
	b.WriteString("<task>\n")
	b.WriteString("Summarize the focus of this idea in a single declarative sentence (<=120 chars).\n")
	b.WriteString("Lead with the verb. Describe what's being done, not why.\n")
	b.WriteString("\n")
	b.WriteString("Respond with one JSON object on a single line. No markdown fence. Schema:\n")
	b.WriteString("\n")
	b.WriteString("  {\"summary\": \"<sentence>\", \"warnings\": [...], \"suggested_resources\": [{\"type\": \"...\", \"url\": \"...\", \"label\": \"...\"}, ...]}\n")
	b.WriteString("\n")
	b.WriteString("`summary` is required. `warnings` is optional; include one entry per missing or\n")
	b.WriteString("unusable input (e.g. transcript path didn't exist, body was empty, repo log was\n")
	b.WriteString("empty). Never describe missing context inside `summary` itself — warnings is the\n")
	b.WriteString("only channel for that.\n")
	b.WriteString("\n")
	b.WriteString("`suggested_resources` is optional. Include it when you spot links in the transcript\n")
	b.WriteString("or context that belong on the idea — GitHub PRs, Jira issues, Notion pages,\n")
	b.WriteString("WebFetch URLs, linked repos, or feature flags. The live agent should have tracked\n")
	b.WriteString("most of these already; this is a safety-net pass for any it missed. Each entry:\n")
	b.WriteString("  {\"type\": \"<resource-type>\", \"url\": \"<canonical-url>\", \"label\": \"<short label>\"}\n")
	b.WriteString("Omit the field entirely if there is nothing new to suggest.\n")
	b.WriteString("</task>\n\n")

	// <idea ...> with lifecycle attrs + body wrapped with voice annotation.
	b.WriteString("<idea")
	b.WriteString(" name=\"")
	b.WriteString(xmlAttr(in.IdeaName))
	b.WriteString("\"")
	if in.Type != "" {
		b.WriteString(" type=\"")
		b.WriteString(xmlAttr(in.Type))
		b.WriteString("\"")
	}
	if in.Status != "" {
		b.WriteString(" status=\"")
		b.WriteString(xmlAttr(in.Status))
		b.WriteString("\"")
	}
	if in.Phase != "" {
		b.WriteString(" phase=\"")
		b.WriteString(xmlAttr(in.Phase))
		b.WriteString("\"")
	}
	b.WriteString(">\n")
	if strings.TrimSpace(in.IdeaBody) != "" {
		b.WriteString("  <body voice=\"user-first-person-rewrite-as-declarative\">\n")
		b.WriteString(in.IdeaBody)
		b.WriteString("\n  </body>\n")
	}
	b.WriteString("</idea>\n\n")

	// <context> with whichever pointer paths are populated.
	hasCtx := in.IdeaDir != "" || len(in.RepoPaths) > 0 || in.TranscriptPath != "" || in.VscreenPath != ""
	if hasCtx {
		b.WriteString("<context>\n")
		if in.IdeaDir != "" {
			b.WriteString("  <idea-dir>")
			b.WriteString(in.IdeaDir)
			b.WriteString("</idea-dir>\n")
		}
		for _, p := range in.RepoPaths {
			b.WriteString("  <repo>")
			b.WriteString(p)
			b.WriteString("</repo>\n")
		}
		if in.TranscriptPath != "" {
			b.WriteString("  <transcript role=\"progress-evidence\">")
			b.WriteString(in.TranscriptPath)
			b.WriteString("</transcript>\n")
		}
		if in.VscreenPath != "" {
			b.WriteString("  <vscreen role=\"last-on-screen-state\">")
			b.WriteString(in.VscreenPath)
			b.WriteString("</vscreen>\n")
		}
		b.WriteString("</context>\n\n")

		b.WriteString("<research-hints>\n")
		b.WriteString("- `git -C <repo> log -10 --oneline` for recent activity per linked repo\n")
		b.WriteString("- `Read` the transcript file for the latest agent turns\n")
		b.WriteString("- `Read` the vscreen snapshot for what was on screen when the session ended\n")
		b.WriteString("- Skip research if the body alone answers the question\n")
		b.WriteString("</research-hints>\n\n")
	}

	if strings.TrimSpace(in.TranscriptTail) != "" {
		b.WriteString("<transcript-tail>\n")
		b.WriteString(in.TranscriptTail)
		b.WriteString("\n</transcript-tail>\n\n")
	}

	b.WriteString("Output only the single-line JSON object.")
	return b.String()
}

// xmlAttr quotes a string for inclusion in an XML attribute. We don't
// need a full encoder — idea names / types / statuses are short and
// won't contain control bytes — but we do need to neutralize the
// closing-quote / ampersand bytes that would break the tag.
func xmlAttr(s string) string {
	r := strings.NewReplacer(`&`, `&amp;`, `"`, `&quot;`, `<`, `&lt;`, `>`, `&gt;`)
	return r.Replace(s)
}
