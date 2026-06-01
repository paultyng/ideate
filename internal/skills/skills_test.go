package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNames_HasCanonicalBundle(t *testing.T) {
	t.Parallel()
	names := Names()
	if len(names) == 0 {
		t.Fatal("expected at least one canonical skill")
	}
	want := []string{"summarize-ideas", "work-idea"}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing canonical skill %q in %v", w, names)
		}
	}
}

func TestInstallMissing_WritesAbsentFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	installed, err := InstallMissing(dir)
	if err != nil {
		t.Fatalf("InstallMissing: %v", err)
	}
	if len(installed) != len(Names()) {
		t.Errorf("installed %d, want %d", len(installed), len(Names()))
	}
	for _, name := range Names() {
		path := filepath.Join(dir, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s, stat err: %v", path, err)
		}
	}
}

func TestInstallMissing_LeavesUserEditsAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	name := Names()[0]
	path := filepath.Join(dir, ".claude", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	userContent := []byte("user-edited content; auto-install must not touch")
	if err := os.WriteFile(path, userContent, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	installed, err := InstallMissing(dir)
	if err != nil {
		t.Fatalf("InstallMissing: %v", err)
	}
	for _, n := range installed {
		if n == name {
			t.Errorf("auto-install overwrote existing skill %q", name)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after install: %v", err)
	}
	if string(got) != string(userContent) {
		t.Errorf("user content was rewritten; got %q, want %q", got, userContent)
	}
}

func TestList_StatusesReflectDiskState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// All missing up front.
	for _, s := range List(dir) {
		if s.Status != StatusMissing {
			t.Errorf("%s: status = %q, want missing", s.Name, s.Status)
		}
	}

	// Install canonical → up-to-date.
	if _, err := InstallMissing(dir); err != nil {
		t.Fatalf("InstallMissing: %v", err)
	}
	for _, s := range List(dir) {
		if s.Status != StatusUpToDate {
			t.Errorf("%s: status = %q, want up-to-date", s.Name, s.Status)
		}
		if s.OnDiskSHA == "" || s.OnDiskSHA != s.CanonicalSHA {
			t.Errorf("%s: sha mismatch %q vs %q", s.Name, s.OnDiskSHA, s.CanonicalSHA)
		}
	}

	// Modify one → modified.
	target := Names()[0]
	path := filepath.Join(dir, ".claude", "skills", target, "SKILL.md")
	if err := os.WriteFile(path, []byte("local edits"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	for _, s := range List(dir) {
		if s.Name == target {
			if s.Status != StatusModified {
				t.Errorf("%s: status = %q, want modified", s.Name, s.Status)
			}
		}
	}
}

func TestReset_RewritesFromCanonical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := InstallMissing(dir); err != nil {
		t.Fatalf("InstallMissing: %v", err)
	}
	target := Names()[0]
	path := filepath.Join(dir, ".claude", "skills", target, "SKILL.md")
	if err := os.WriteFile(path, []byte("user edits"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	// Add a stray file in the skill dir — reset should nuke the whole dir.
	stray := filepath.Join(filepath.Dir(path), "stray.txt")
	if err := os.WriteFile(stray, []byte("stray"), 0o644); err != nil {
		t.Fatalf("stray: %v", err)
	}

	done, err := Reset(dir, target)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if len(done) != 1 || done[0] != target {
		t.Errorf("done = %v, want [%s]", done, target)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("stray file survived reset: %v", err)
	}
	for _, s := range List(dir) {
		if s.Name == target && s.Status != StatusUpToDate {
			t.Errorf("post-reset %s status = %q, want up-to-date", target, s.Status)
		}
	}
}

func TestReset_UnknownSkill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Reset(dir, "nonexistent-canonical"); err == nil {
		t.Error("expected error for unknown skill, got nil")
	}
}

func TestReset_AllRewritesEverything(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := InstallMissing(dir); err != nil {
		t.Fatalf("InstallMissing: %v", err)
	}
	for _, n := range Names() {
		path := filepath.Join(dir, ".claude", "skills", n, "SKILL.md")
		if err := os.WriteFile(path, []byte("edits"), 0o644); err != nil {
			t.Fatalf("modify %s: %v", n, err)
		}
	}
	done, err := Reset(dir, "")
	if err != nil {
		t.Fatalf("Reset all: %v", err)
	}
	if len(done) != len(Names()) {
		t.Errorf("done = %d, want %d", len(done), len(Names()))
	}
	for _, s := range List(dir) {
		if s.Status != StatusUpToDate {
			t.Errorf("%s: status = %q, want up-to-date", s.Name, s.Status)
		}
	}
}
