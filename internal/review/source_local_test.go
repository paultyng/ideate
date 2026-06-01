package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// gitCmd runs a git command in the given dir for test setup. Production code
// uses go-git; tests use the git CLI to bootstrap repos with realistic state.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLocalSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) { gitCmd(t, dir, args...) }

	// Initialize repo.
	git("init", "-b", "main")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")

	// Create initial files on main.
	writeFile(t, filepath.Join(dir, "existing.go"), "package main\n\nvar x = 1\n")
	writeFile(t, filepath.Join(dir, "todelete.txt"), "this will be deleted\n")
	git("add", ".")
	git("commit", "-m", "initial")

	// Create feature branch with changes.
	git("checkout", "-b", "feature")
	writeFile(t, filepath.Join(dir, "existing.go"), "package main\n\nvar x = 2\n")
	writeFile(t, filepath.Join(dir, "newfile.ts"), "const a: number = 1;\nexport default a;\n")
	git("rm", "todelete.txt")
	git("add", ".")
	git("commit", "-m", "feature changes")

	// Run the diff.
	src := &LocalSource{RepoPath: dir, Base: "main", Head: "feature"}
	result, err := src.GetDiff(ctx)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}

	if result.Base != "main" {
		t.Errorf("Base: expected %q, got %q", "main", result.Base)
	}
	if result.Head != "feature" {
		t.Errorf("Head: expected %q, got %q", "feature", result.Head)
	}

	if len(result.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result.Files))
	}

	// Sort by NewName for deterministic assertions.
	sort.Slice(result.Files, func(i, j int) bool {
		return result.Files[i].NewName < result.Files[j].NewName
	})

	// existing.go — modified
	existing := result.Files[0]
	if existing.NewName != "existing.go" {
		t.Errorf("expected existing.go, got %q", existing.NewName)
	}
	if existing.Status != "modified" {
		t.Errorf("existing.go status: expected %q, got %q", "modified", existing.Status)
	}
	if existing.OldContent != "package main\n\nvar x = 1\n" {
		t.Errorf("existing.go OldContent: %q", existing.OldContent)
	}
	if existing.NewContent != "package main\n\nvar x = 2\n" {
		t.Errorf("existing.go NewContent: %q", existing.NewContent)
	}
	if existing.Language != "go" {
		t.Errorf("existing.go Language: expected %q, got %q", "go", existing.Language)
	}
	if existing.Hunks == "" {
		t.Error("existing.go Hunks should not be empty")
	}

	// newfile.ts — added
	newfile := result.Files[1]
	if newfile.NewName != "newfile.ts" {
		t.Errorf("expected newfile.ts, got %q", newfile.NewName)
	}
	if newfile.Status != "added" {
		t.Errorf("newfile.ts status: expected %q, got %q", "added", newfile.Status)
	}
	if newfile.OldContent != "" {
		t.Errorf("newfile.ts OldContent should be empty, got %q", newfile.OldContent)
	}
	if newfile.NewContent != "const a: number = 1;\nexport default a;\n" {
		t.Errorf("newfile.ts NewContent: %q", newfile.NewContent)
	}
	if newfile.Language != "typescript" {
		t.Errorf("newfile.ts Language: expected %q, got %q", "typescript", newfile.Language)
	}
	if newfile.Hunks == "" {
		t.Error("newfile.ts Hunks should not be empty")
	}

	// todelete.txt — deleted
	deleted := result.Files[2]
	if deleted.NewName != "todelete.txt" {
		t.Errorf("expected todelete.txt, got %q", deleted.NewName)
	}
	if deleted.Status != "deleted" {
		t.Errorf("todelete.txt status: expected %q, got %q", "deleted", deleted.Status)
	}
	if deleted.OldContent != "this will be deleted\n" {
		t.Errorf("todelete.txt OldContent: %q", deleted.OldContent)
	}
	if deleted.NewContent != "" {
		t.Errorf("todelete.txt NewContent should be empty, got %q", deleted.NewContent)
	}
	if deleted.Hunks == "" {
		t.Error("todelete.txt Hunks should not be empty")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestLocalSource_TruncatesLargeFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) { gitCmd(t, dir, args...) }

	git("init", "-b", "main")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")

	writeFile(t, filepath.Join(dir, "small.txt"), "v1\n")
	git("add", ".")
	git("commit", "-m", "initial")

	git("checkout", "-b", "feature")
	// Write a file larger than MaxFileBytes.
	huge := strings.Repeat("a", MaxFileBytes+1024)
	writeFile(t, filepath.Join(dir, "small.txt"), huge)
	git("add", ".")
	git("commit", "-m", "huge")

	src := &LocalSource{RepoPath: dir, Base: "main", Head: "feature"}
	result, err := src.GetDiff(ctx)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	f := result.Files[0]
	if !f.Truncated {
		t.Error("expected Truncated to be true for oversized file")
	}
	if f.NewContent != "" {
		t.Errorf("expected empty NewContent when truncated, got %d bytes", len(f.NewContent))
	}
}

func TestLocalSource_RejectsTooManyFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) { gitCmd(t, dir, args...) }

	git("init", "-b", "main")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "seed.txt"), "x\n")
	git("add", ".")
	git("commit", "-m", "initial")

	git("checkout", "-b", "feature")
	for i := range MaxFiles + 5 {
		writeFile(t, filepath.Join(dir, "f"+itoa(i)+".txt"), "n\n")
	}
	git("add", ".")
	git("commit", "-m", "many")

	src := &LocalSource{RepoPath: dir, Base: "main", Head: "feature"}
	_, err := src.GetDiff(ctx)
	if !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("expected ErrDiffTooLarge, got %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
