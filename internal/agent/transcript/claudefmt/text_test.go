package claudefmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLastNTurns_MixedShapes(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"first question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"first answer"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"output"}]}}`,
		`{"type":"user","message":{"role":"user","content":"second question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"},{"type":"text","text":"answer"}]}}`,
	)
	got, err := LastNTurns(path, 3)
	if err != nil {
		t.Fatalf("LastNTurns: %v", err)
	}
	want := strings.Join([]string{
		"assistant: first answer",
		"user: second question",
		"assistant: second answer",
	}, "\n\n")
	if got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestLastNTurns_EmptyOrTooShort(t *testing.T) {
	t.Parallel()
	// Empty file.
	path := writeTranscript(t)
	got, err := LastNTurns(path, 5)
	if err != nil {
		t.Fatalf("LastNTurns: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}

	// Fewer turns than requested — returns all of them.
	path2 := writeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"only one"}}`,
	)
	got2, _ := LastNTurns(path2, 10)
	if got2 != "user: only one" {
		t.Errorf("got %q", got2)
	}
}

func TestLastNTurns_SkipsMetaRecords(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"system reminder, skip me"}}`,
		`{"type":"user","message":{"role":"user","content":"real question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"real answer"}]}}`,
	)
	got, err := LastNTurns(path, 5)
	if err != nil {
		t.Fatalf("LastNTurns: %v", err)
	}
	if strings.Contains(got, "system reminder") {
		t.Errorf("meta record leaked into output: %q", got)
	}
	if !strings.Contains(got, "real question") || !strings.Contains(got, "real answer") {
		t.Errorf("real records missing: %q", got)
	}
}

func TestLastNTurns_TolerantOfBadLines(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{garbage`,
		`{"type":"user","message":{"role":"user","content":"good one"}}`,
		``,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"good answer"}]}}`,
	)
	got, _ := LastNTurns(path, 5)
	want := "user: good one\n\nassistant: good answer"
	if got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestLastNTurns_ZeroN(t *testing.T) {
	t.Parallel()
	path := writeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)
	got, err := LastNTurns(path, 0)
	if err != nil {
		t.Fatalf("LastNTurns: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for n=0", got)
	}
}
