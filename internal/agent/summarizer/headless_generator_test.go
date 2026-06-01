package summarizer

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/agent/headless"
)

// fakeRunner returns a fixed reply as a single text_delta + result
// NDJSON stream so DrainText sees the full body and the runner pipe
// closes cleanly.
type fakeRunner struct {
	reply string
}

func (f fakeRunner) Run(_ context.Context, _ string, _ headless.Opts) (io.ReadCloser, error) {
	// FakeEvents takes raw text chunks and synthesizes the wire
	// frames; closing the body is handled by the headless package.
	r := &headless.FakeRunner{Events: headless.FakeEvents(f.reply)}
	return r.Run(context.Background(), "", headless.Opts{})
}

func newCapturingLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

func TestBuildHeadlessPrompt_StructureAndVoiceCues(t *testing.T) {
	t.Parallel()

	got := buildHeadlessPrompt(GenerateInput{
		IdeaName:       "Migrate import pipeline",
		IdeaBody:       "I want to swap the importer over to batched writes.",
		TranscriptTail: "user: status?\nassistant: half-done",
		SessionUUID:    "u-1",
		Status:         "active",
		Phase:          "implementation",
		Type:           "feature",
		IdeaDir:        "/ideas/migrate-import-pipeline",
		RepoPaths:      []string{"/ideas/migrate-import-pipeline/repos/api"},
		TranscriptPath: "/claude-projects/encoded/u-1.jsonl",
		VscreenPath:    "/ideas/migrate-import-pipeline/sessions/u-1.vscreen.ansi",
	})

	// Required substrings: <idea> lifecycle attrs, body voice annotation,
	// context block with every pointer, research-hints, transcript-tail,
	// and the JSON output schema fragment.
	mustContain := []string{
		`<idea name="Migrate import pipeline"`,
		`type="feature"`,
		`status="active"`,
		`phase="implementation"`,
		`<body voice="user-first-person-rewrite-as-declarative">`,
		`<idea-dir>/ideas/migrate-import-pipeline</idea-dir>`,
		`<repo>/ideas/migrate-import-pipeline/repos/api</repo>`,
		`<transcript role="progress-evidence">/claude-projects/encoded/u-1.jsonl</transcript>`,
		`<vscreen role="last-on-screen-state">/ideas/migrate-import-pipeline/sessions/u-1.vscreen.ansi</vscreen>`,
		`<research-hints>`,
		`<transcript-tail>`,
		`{"summary": "<sentence>", "warnings": [...], "suggested_resources": [{"type": "...", "url": "...", "label": "..."}, ...]}`,
		`Never describe missing context inside ` + "`summary`" + ` itself`,
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("prompt missing %q:\n%s", s, got)
		}
	}
}

func TestBuildHeadlessPrompt_EmptyOptionalsAreOmitted(t *testing.T) {
	t.Parallel()

	got := buildHeadlessPrompt(GenerateInput{
		IdeaName: "Bare Idea",
		// No body, no transcript, no paths, no lifecycle attrs.
	})

	// Required: <idea name="..."> tag with no attrs.
	if !strings.Contains(got, `<idea name="Bare Idea">`) {
		t.Errorf("missing bare idea tag:\n%s", got)
	}

	// Optional tags must NOT render when their data is empty.
	forbidden := []string{
		`<body`,
		`<context>`,
		`<idea-dir>`,
		`<repo>`,
		`<transcript role=`,
		`<vscreen role=`,
		`<research-hints>`,
		`<transcript-tail>`,
		`type="`,
		`status="`,
		`phase="`,
	}
	for _, s := range forbidden {
		if strings.Contains(got, s) {
			t.Errorf("prompt unexpectedly contains %q (should be omitted when empty):\n%s", s, got)
		}
	}
}

func TestHeadlessGenerator_ParsesJSONAndLogsWarnings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		reply       string
		wantSummary string
		wantErr     bool
		wantLogs    []string // substrings that must appear in JSON log lines
		forbidLogs  []string // substrings that must NOT appear
	}{
		{
			name:        "valid JSON with warnings",
			reply:       `{"summary":"Migrating import pipeline to batched writes","warnings":["transcript path was empty","repos/api had zero recent commits"]}`,
			wantSummary: "Migrating import pipeline to batched writes",
			wantLogs: []string{
				`"msg":"summarizer.warning"`,
				`"text":"transcript path was empty"`,
				`"text":"repos/api had zero recent commits"`,
			},
			forbidLogs: []string{`"msg":"summarizer.parse_fallback"`},
		},
		{
			name:        "valid JSON empty warnings",
			reply:       `{"summary":"Drafting decision doc","warnings":[]}`,
			wantSummary: "Drafting decision doc",
			forbidLogs:  []string{`"msg":"summarizer.warning"`, `"msg":"summarizer.parse_fallback"`},
		},
		{
			name:        "valid JSON warnings field absent",
			reply:       `{"summary":"Stuck on review of PR #42"}`,
			wantSummary: "Stuck on review of PR #42",
			forbidLogs:  []string{`"msg":"summarizer.warning"`, `"msg":"summarizer.parse_fallback"`},
		},
		{
			name:        "malformed reply falls back to raw line",
			reply:       `Migrating thing — but actually idk`,
			wantSummary: "Migrating thing — but actually idk",
			wantLogs: []string{
				`"msg":"summarizer.parse_fallback"`,
				`"reply_preview":"Migrating thing — but actually idk"`,
			},
		},
		{
			name:        "fenced JSON parses cleanly",
			reply:       "```json\n{\"summary\":\"Migrating import pipeline\",\"warnings\":[\"transcript empty\"]}\n```",
			wantSummary: "Migrating import pipeline",
			wantLogs:    []string{`"text":"transcript empty"`},
			forbidLogs:  []string{`"msg":"summarizer.parse_fallback"`},
		},
		{
			name:        "prose preamble before JSON parses cleanly",
			reply:       `I'll inspect the transcript first.{"summary":"Stuck on review","warnings":[]}`,
			wantSummary: "Stuck on review",
			forbidLogs:  []string{`"msg":"summarizer.parse_fallback"`, `"msg":"summarizer.warning"`},
		},
		{
			name:        "fence with no language tag",
			reply:       "```\n{\"summary\":\"Drafting decision doc\"}\n```",
			wantSummary: "Drafting decision doc",
			forbidLogs:  []string{`"msg":"summarizer.parse_fallback"`},
		},
		{
			name:        "JSON without summary field falls back",
			reply:       `{"warnings":["body empty"]}`,
			wantSummary: `{"warnings":["body empty"]}`,
			wantLogs:    []string{`"msg":"summarizer.parse_fallback"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger, buf := newCapturingLogger(t)
			gen := HeadlessGenerator{
				Runner: fakeRunner{reply: tc.reply},
				Logger: logger,
			}
			got, err := gen.Generate(context.Background(), GenerateInput{
				IdeaName:    "Test",
				SessionUUID: "sess-1",
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got.Line != tc.wantSummary {
				t.Errorf("summary = %q, want %q", got.Line, tc.wantSummary)
			}
			logs := buf.String()
			for _, want := range tc.wantLogs {
				if !strings.Contains(logs, want) {
					t.Errorf("logs missing %q:\n%s", want, logs)
				}
			}
			for _, forbid := range tc.forbidLogs {
				if strings.Contains(logs, forbid) {
					t.Errorf("logs unexpectedly contain %q:\n%s", forbid, logs)
				}
			}
		})
	}
}

func TestDefaultSystemPrompt_ForbidsFirstPersonAndAnchorsLifecycle(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"first-person pronouns",
		"I, me, my, we",
		"VOICE",
		"INPUT PRIORITY",
		`"summary"`,
		`"warnings"`,
		"NEVER describe missing context inside summary",
	} {
		if !strings.Contains(defaultSystemPrompt, want) {
			t.Errorf("defaultSystemPrompt missing %q", want)
		}
	}
}

func TestBuildHeadlessPrompt_XmlAttrEscapes(t *testing.T) {
	t.Parallel()

	got := buildHeadlessPrompt(GenerateInput{
		IdeaName: `Quote " in Name & also <bracket>`,
	})
	// The closing-quote byte must be neutralized so the tag stays parseable
	// by anything trying to read the attrs back. Same for `&` / `<` / `>`.
	if !strings.Contains(got, `name="Quote &quot; in Name &amp; also &lt;bracket&gt;"`) {
		t.Errorf("attr escape failed:\n%s", got)
	}
}

func TestBuildHeadlessPrompt_MentionsSuggestedResources(t *testing.T) {
	t.Parallel()
	got := buildHeadlessPrompt(GenerateInput{
		IdeaName: "Anything",
		Status:   "active",
	})
	// The schema fragment must enumerate the new field.
	if !strings.Contains(got, "suggested_resources") {
		t.Errorf("prompt missing %q:\n%s", "suggested_resources", got)
	}
	// Triggers parallel the live <resources> system-prompt block.
	for _, want := range []string{"PR", "Jira", "Notion"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing trigger %q:\n%s", want, got)
		}
	}
}
