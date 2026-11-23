package store

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/model"
)

const (
	legacyAlpha = "---\nname: Alpha\ncreated: 2026-01-10T08:00:00Z\nstatus: paused\nupdated: 2026-04-01T00:00:00Z\npause_until: 2026-05-01T00:00:00Z\nresources:\n    - type: web\n      url: https://example.com/a\n      label: A\n---\nAlpha body.\n"
	legacyBeta  = "---\nname: Beta\nstatus: archived\nupdated: 2026-03-01T12:30:00Z\n---\n# Beta\n\nBody.\n"
	okfGamma    = "---\ntype: Ideate Idea\ntitle: Gamma\nupdated: 2026-04-01T00:00:00Z\n---\nGamma.\n"
	summaryJSON = `{"line":"One-line intent.","generated_at":"2026-04-02T10:00:00Z","source_session_uuid":"abc"}`
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func mustParse(t *testing.T, content string) *model.Idea {
	t.Helper()
	idea, err := model.ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("ParseIdeaFile: %v", err)
	}
	return idea
}

// assertSameIdea checks invariant 3: everything but Description/Generated
// survives migration.
func assertSameIdea(t *testing.T, before, after *model.Idea) {
	t.Helper()
	if after.Name != before.Name || after.Status != before.Status {
		t.Errorf("name/status = %q/%q, want %q/%q", after.Name, after.Status, before.Name, before.Status)
	}
	if !after.Created.Equal(before.Created) || !after.Updated.Equal(before.Updated) {
		t.Errorf("created/updated = %v/%v, want %v/%v", after.Created, after.Updated, before.Created, before.Updated)
	}
	if (after.PauseUntil == nil) != (before.PauseUntil == nil) ||
		(after.PauseUntil != nil && !after.PauseUntil.Equal(*before.PauseUntil)) {
		t.Errorf("pause_until = %v, want %v", after.PauseUntil, before.PauseUntil)
	}
	if !reflect.DeepEqual(after.Resources, before.Resources) {
		t.Errorf("resources = %+v, want %+v", after.Resources, before.Resources)
	}
	wantBody := before.Body
	if !strings.HasSuffix(wantBody, "\n") {
		wantBody += "\n"
	}
	if after.Body != wantBody {
		t.Errorf("body = %q, want %q", after.Body, wantBody)
	}
}

// backupFiles returns every regular file under dir, relative to dir.
func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func backupEntries(t *testing.T, ideasDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(ideasDir, ".backups"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func failedKeys(r MigrationReport) []string {
	var keys []string
	for k := range r.Failed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}

func TestMigrateToOKF_ConvertsLegacyAndBacksUpOnlyChangedFiles(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)

	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)
	writeTestFile(t, filepath.Join(dir, "beta", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, "beta", "history.jsonl"), "{\"event\":\"created\"}\n")
	writeTestFile(t, filepath.Join(dir, "gamma", "idea.md"), okfGamma)
	writeTestFile(t, filepath.Join(dir, "alpha", "repos", "r", "README.md"), "repo file\n")
	writeTestFile(t, filepath.Join(dir, "alpha", "repos", "r", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, "index.md"), "---\nokf_version: \"0.2\"\n---\nroot index\n")
	writeTestFile(t, filepath.Join(dir, ".worktrees", "wt", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, ".hidden", "idea.md"), legacyAlpha)

	report, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("MigrateToOKF: %v", err)
	}
	if report.Migrated != 2 || report.Folded != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v, want Migrated=2 Folded=0 no failures", report)
	}
	if report.BackupDir == "" || !strings.HasPrefix(filepath.Base(report.BackupDir), "okf-") {
		t.Fatalf("BackupDir = %q, want .backups/okf-<ts>", report.BackupDir)
	}
	if filepath.Dir(report.BackupDir) != filepath.Join(dir, ".backups") {
		t.Errorf("BackupDir parent = %q, want %q", filepath.Dir(report.BackupDir), filepath.Join(dir, ".backups"))
	}

	for slug, original := range map[string]string{"alpha": legacyAlpha, "beta": legacyBeta} {
		got := readTestFile(t, filepath.Join(dir, slug, "idea.md"))
		legacy, err := model.IsLegacyIdeaFile([]byte(got))
		if err != nil || legacy {
			t.Errorf("%s: IsLegacyIdeaFile = %v, %v; want false, nil", slug, legacy, err)
		}
		assertSameIdea(t, mustParse(t, original), mustParse(t, got))
		if b := readTestFile(t, filepath.Join(report.BackupDir, slug, "idea.md")); b != original {
			t.Errorf("%s backup = %q, want original bytes", slug, b)
		}
	}

	if got := readTestFile(t, filepath.Join(dir, "gamma", "idea.md")); got != okfGamma {
		t.Errorf("OKF idea changed:\n%s", got)
	}
	want := []string{"alpha/idea.md", "beta/idea.md", "index.md"}
	if got := backupFiles(t, report.BackupDir); !slices.Equal(got, want) {
		t.Errorf("backup files = %v, want %v", got, want)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "alpha", "repos", "r", "README.md"): "repo file\n",
		filepath.Join(dir, "alpha", "repos", "r", "idea.md"):   legacyBeta,
		filepath.Join(dir, ".worktrees", "wt", "idea.md"):      legacyBeta,
		filepath.Join(dir, ".hidden", "idea.md"):               legacyAlpha,
		filepath.Join(dir, "beta", "history.jsonl"):            "{\"event\":\"created\"}\n",
	} {
		if got := readTestFile(t, path); got != content {
			t.Errorf("%s changed to %q", path, got)
		}
	}
	if fileExists(filepath.Join(dir, "alpha", "history.jsonl")) {
		t.Error("migration appended history for alpha")
	}
}

func TestMigrateToOKF_FoldsSummary(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)
	genAt := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)

	// Legacy, empty description: fold.
	writeTestFile(t, filepath.Join(dir, "legacy", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, "legacy", "summary.json"), summaryJSON)
	// OKF with a description: keep it, still remove summary.json.
	withDesc := "---\ntype: Ideate Idea\ntitle: D\ndescription: Keep me.\nupdated: 2026-04-01T00:00:00Z\n---\nD.\n"
	writeTestFile(t, filepath.Join(dir, "described", "idea.md"), withDesc)
	writeTestFile(t, filepath.Join(dir, "described", "summary.json"), summaryJSON)
	// OKF, no description, only a summary.json: fold.
	writeTestFile(t, filepath.Join(dir, "okfonly", "idea.md"), okfGamma)
	writeTestFile(t, filepath.Join(dir, "okfonly", "summary.json"), summaryJSON)
	// Empty line: no fold, summary.json removed.
	writeTestFile(t, filepath.Join(dir, "emptyline", "idea.md"), okfGamma)
	writeTestFile(t, filepath.Join(dir, "emptyline", "summary.json"), `{"line":"","generated_at":"2026-04-02T10:00:00Z"}`)

	report, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("MigrateToOKF: %v", err)
	}
	if report.Migrated != 4 || report.Folded != 2 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v, want Migrated=4 Folded=2 no failures", report)
	}

	for _, slug := range []string{"legacy", "okfonly"} {
		got := mustParse(t, readTestFile(t, filepath.Join(dir, slug, "idea.md")))
		if got.Description != "One-line intent." {
			t.Errorf("%s Description = %q, want summary line", slug, got.Description)
		}
		if !got.Generated.Equal(genAt) {
			t.Errorf("%s Generated = %v, want %v", slug, got.Generated, genAt)
		}
	}
	gotDesc := mustParse(t, readTestFile(t, filepath.Join(dir, "described", "idea.md")))
	if gotDesc.Description != "Keep me." {
		t.Errorf("described Description = %q, want unchanged", gotDesc.Description)
	}
	if !gotDesc.Generated.IsZero() {
		t.Errorf("described Generated = %v, want zero (not folded)", gotDesc.Generated)
	}
	if got := mustParse(t, readTestFile(t, filepath.Join(dir, "emptyline", "idea.md"))); got.Description != "" {
		t.Errorf("emptyline Description = %q, want empty", got.Description)
	}
	for _, slug := range []string{"legacy", "described", "okfonly", "emptyline"} {
		if fileExists(filepath.Join(dir, slug, "summary.json")) {
			t.Errorf("%s/summary.json not removed", slug)
		}
		if !fileExists(filepath.Join(report.BackupDir, slug, "summary.json")) {
			t.Errorf("%s/summary.json not backed up", slug)
		}
	}
	if !mustParse(t, readTestFile(t, filepath.Join(dir, "legacy", "idea.md"))).Updated.Equal(mustParse(t, legacyBeta).Updated) {
		t.Error("Updated changed by fold")
	}
}

func TestMigrateToOKF_SecondRunIsNoOpAfterFailures(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)

	guardFailed := "---\nname: Bad\nstatus: active\ncreated: not-a-date\n---\nBody.\n"
	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)
	writeTestFile(t, filepath.Join(dir, "badsum", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, "badsum", "summary.json"), "{not json")
	writeTestFile(t, filepath.Join(dir, "guard", "idea.md"), guardFailed)
	writeTestFile(t, filepath.Join(dir, "folded", "idea.md"), okfGamma)
	writeTestFile(t, filepath.Join(dir, "folded", "summary.json"), summaryJSON)

	first, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("first MigrateToOKF: %v", err)
	}
	if first.Migrated != 3 || first.BackupDir == "" {
		t.Fatalf("first report = %+v, want Migrated=3 with backup", first)
	}
	wantFailed := []string{"badsum", "guard"}
	if got := failedKeys(first); !slices.Equal(got, wantFailed) {
		t.Fatalf("first Failed = %v, want %v", got, wantFailed)
	}
	// Malformed summary.json: idea still converted, file kept, not backed up.
	if legacy, _ := model.IsLegacyIdeaFile([]byte(readTestFile(t, filepath.Join(dir, "badsum", "idea.md")))); legacy {
		t.Error("badsum idea.md still legacy")
	}
	if got := readTestFile(t, filepath.Join(dir, "badsum", "summary.json")); got != "{not json" {
		t.Errorf("malformed summary.json changed to %q", got)
	}
	if fileExists(filepath.Join(first.BackupDir, "guard")) {
		t.Error("guard-failed idea backed up")
	}

	second, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("second MigrateToOKF: %v", err)
	}
	if second.BackupDir != "" || second.Migrated != 0 || second.Folded != 0 {
		t.Errorf("second report = %+v, want no-op", second)
	}
	if got := failedKeys(second); !slices.Equal(got, wantFailed) {
		t.Errorf("second Failed = %v, want %v", got, wantFailed)
	}
	if got := backupEntries(t, dir); len(got) != 1 {
		t.Errorf(".backups entries = %v, want exactly one", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "guard", "idea.md")); got != guardFailed {
		t.Errorf("guard-failed idea.md changed:\n%s", got)
	}
}

func TestMigrateToOKF_FailuresLeaveBytesAndSkipBackup(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"unparseable idea.md":      "---\nname: [unclosed\nstatus: active\n---\nBody.\n",
		"scalar created dropped":   "---\nname: X\nstatus: active\ncreated: not-a-date\n---\nBody.\n",
		"malformed resources":      "---\nname: X\nstatus: active\nresources:\n    - just-a-string\n---\nBody.\n",
		"non-string name":          "---\nname: 123\nstatus: active\n---\nBody.\n",
		"garbage pause_until":      "---\nname: X\nstatus: paused\nupdated: 2026-04-01T00:00:00Z\npause_until: garbage\n---\nBody.\n",
		"malformed okf created at": "---\ntype: Ideate Idea\ntitle: X\nname: X\ncreated:\n    at: [1, 2]\n---\nBody.\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, dir := newTestStore(t)
			writeTestFile(t, filepath.Join(dir, "bad", "idea.md"), content)

			report, err := s.MigrateToOKF(context.Background())
			if err != nil {
				t.Fatalf("MigrateToOKF: %v", err)
			}
			if _, ok := report.Failed["bad"]; !ok || report.Migrated != 0 || report.BackupDir != "" {
				t.Errorf("report = %+v, want bad failed, nothing migrated, no backup", report)
			}
			if got := readTestFile(t, filepath.Join(dir, "bad", "idea.md")); got != content {
				t.Errorf("idea.md changed:\n%s", got)
			}
			if got := backupEntries(t, dir); len(got) != 0 {
				t.Errorf(".backups entries = %v, want none", got)
			}
		})
	}

	t.Run("others still migrate", func(t *testing.T) {
		t.Parallel()
		s, dir := newTestStore(t)
		bad := cases["unparseable idea.md"]
		writeTestFile(t, filepath.Join(dir, "bad", "idea.md"), bad)
		writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)

		report, err := s.MigrateToOKF(context.Background())
		if err != nil {
			t.Fatalf("MigrateToOKF: %v", err)
		}
		if report.Migrated != 1 || len(report.Failed) != 1 || report.Failed["bad"] == nil {
			t.Errorf("report = %+v, want alpha migrated and bad failed", report)
		}
		if got := readTestFile(t, filepath.Join(dir, "bad", "idea.md")); got != bad {
			t.Errorf("bad idea.md changed:\n%s", got)
		}
		if fileExists(filepath.Join(report.BackupDir, "bad")) {
			t.Error("failed idea backed up")
		}
	})
}

func TestMigrateToOKF_BodyShapes(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)
	empty := "---\nname: Empty\nstatus: active\nupdated: 2026-04-01T00:00:00Z\n---\n"
	noNewline := "---\nname: NoNL\nstatus: active\nupdated: 2026-04-01T00:00:00Z\n---\nNo trailing newline"
	writeTestFile(t, filepath.Join(dir, "empty", "idea.md"), empty)
	writeTestFile(t, filepath.Join(dir, "nonl", "idea.md"), noNewline)

	report, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("MigrateToOKF: %v", err)
	}
	if report.Migrated != 2 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v, want both migrated", report)
	}
	assertSameIdea(t, mustParse(t, empty), mustParse(t, readTestFile(t, filepath.Join(dir, "empty", "idea.md"))))
	assertSameIdea(t, mustParse(t, noNewline), mustParse(t, readTestFile(t, filepath.Join(dir, "nonl", "idea.md"))))
}

func TestMigrateToOKF_LifecycleStates(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)
	fixtures := map[string]string{
		"active":         "---\nname: A\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-04-01T00:00:00Z\npause_until: 2026-05-01\n---\nA.\n",
		"paused-dated":   legacyAlpha,
		"paused-nodate":  "---\nname: P\nstatus: paused\nupdated: 2026-04-01T00:00:00Z\n---\nP.\n",
		"archived":       legacyBeta,
		"unknown-status": "---\nname: U\nstatus: bogus\nupdated: 2026-04-01T00:00:00Z\n---\nU.\n",
		"empty-resource": "---\nname: R\nstatus: active\nresources: []\n---\nR.\n",
	}
	for slug, content := range fixtures {
		writeTestFile(t, filepath.Join(dir, slug, "idea.md"), content)
	}

	report, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("MigrateToOKF: %v", err)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", report.Failed)
	}
	for slug, content := range fixtures {
		got := readTestFile(t, filepath.Join(dir, slug, "idea.md"))
		before, after := mustParse(t, content), mustParse(t, got)
		if slug == "empty-resource" {
			before.Resources = nil
		}
		assertSameIdea(t, before, after)
	}
}

func TestMigrateToOKF_SkipsFileChangedSinceScan(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)
	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)
	writeTestFile(t, filepath.Join(dir, "alpha", "summary.json"), summaryJSON)
	writeTestFile(t, filepath.Join(dir, "beta", "idea.md"), legacyBeta)

	report := MigrationReport{Failed: map[string]error{}}
	plan, err := s.planOKFMigration(report.Failed)
	if err != nil {
		t.Fatal(err)
	}
	changed := legacyAlpha + "edited\n"
	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), changed)

	report, err = s.applyOKFMigration(plan, report)
	if err != nil {
		t.Fatalf("applyOKFMigration: %v", err)
	}
	if report.Migrated != 1 || report.Failed["alpha"] == nil {
		t.Errorf("report = %+v, want beta migrated and alpha failed", report)
	}
	if got := readTestFile(t, filepath.Join(dir, "alpha", "idea.md")); got != changed {
		t.Errorf("changed idea.md overwritten:\n%s", got)
	}
	if !fileExists(filepath.Join(dir, "alpha", "summary.json")) {
		t.Error("summary.json removed for an idea that was not written")
	}
}

func TestMigrateToOKF_CopyErrorLeavesNoPartialBackup(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)
	s, dir := newTestStore(t)
	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)
	indexPath := filepath.Join(dir, "index.md")
	writeTestFile(t, indexPath, "root index\n")
	if err := os.Chmod(indexPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(indexPath, 0o644) })

	if _, err := s.MigrateToOKF(context.Background()); err == nil {
		t.Fatal("MigrateToOKF err = nil, want copy error")
	}
	if got := backupEntries(t, dir); len(got) != 0 {
		t.Errorf(".backups entries = %v, want none", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "alpha", "idea.md")); got != legacyAlpha {
		t.Errorf("idea.md written despite failed backup:\n%s", got)
	}
}

func TestMigrateToOKF_BackupFailureWritesNothing(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)
	s, dir := newTestStore(t)
	writeTestFile(t, filepath.Join(dir, "alpha", "idea.md"), legacyAlpha)
	writeTestFile(t, filepath.Join(dir, "beta", "idea.md"), legacyBeta)
	writeTestFile(t, filepath.Join(dir, "beta", "summary.json"), summaryJSON)
	backups := filepath.Join(dir, ".backups")
	if err := os.Mkdir(backups, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(backups, 0o755) })

	if _, err := s.MigrateToOKF(context.Background()); err == nil {
		t.Fatal("MigrateToOKF err = nil, want backup error")
	}
	if got := readTestFile(t, filepath.Join(dir, "alpha", "idea.md")); got != legacyAlpha {
		t.Errorf("alpha idea.md modified:\n%s", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "beta", "idea.md")); got != legacyBeta {
		t.Errorf("beta idea.md modified:\n%s", got)
	}
	if !fileExists(filepath.Join(dir, "beta", "summary.json")) {
		t.Error("summary.json removed despite failed backup")
	}
}

func TestMigrateToOKF_NothingToDo(t *testing.T) {
	t.Parallel()
	s, dir := newTestStore(t)
	writeTestFile(t, filepath.Join(dir, "gamma", "idea.md"), okfGamma)
	writeTestFile(t, filepath.Join(dir, "notanidea", "notes.txt"), "x")

	report, err := s.MigrateToOKF(context.Background())
	if err != nil {
		t.Fatalf("MigrateToOKF: %v", err)
	}
	if report.BackupDir != "" || report.Migrated != 0 || len(report.Failed) != 0 {
		t.Errorf("report = %+v, want empty", report)
	}
	if fileExists(filepath.Join(dir, ".backups")) {
		t.Error(".backups created with nothing to migrate")
	}
	if got := readTestFile(t, filepath.Join(dir, "gamma", "idea.md")); got != okfGamma {
		t.Errorf("OKF idea changed:\n%s", got)
	}
}
