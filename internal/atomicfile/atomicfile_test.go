package atomicfile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/paultyng/ideate/internal/atomicfile"
)

func TestWrite_CreatesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(target, []byte("hello"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("contents = %q, want %q", got, "hello")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0o600", info.Mode().Perm())
	}
}

func TestWrite_ReplacesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")

	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := atomicfile.Write(target, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("contents = %q, want %q", got, "new")
	}
}

func TestWrite_LeavesNoTempOnSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != "out.json" {
			t.Errorf("leftover file: %s", name)
		}
	}
}

func TestWrite_MissingParentDirReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "missing", "out.json")

	err := atomicfile.Write(target, []byte("x"), 0o600)
	if err == nil {
		t.Fatal("Write succeeded; expected error for missing parent dir")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}
