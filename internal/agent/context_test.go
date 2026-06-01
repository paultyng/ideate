package agent

import (
	"os"
	"testing"
)

func TestCleanupTempFiles(t *testing.T) {
	t.Parallel()

	f1, _ := os.CreateTemp("", "test-cleanup-*")
	f2, _ := os.CreateTemp("", "test-cleanup-*")
	path1, path2 := f1.Name(), f2.Name()
	_ = f1.Close()
	_ = f2.Close()

	cleanupTempFiles([]string{path1, path2, "/nonexistent/path"})

	if _, err := os.Stat(path1); !os.IsNotExist(err) {
		t.Errorf("file1 still exists")
	}
	if _, err := os.Stat(path2); !os.IsNotExist(err) {
		t.Errorf("file2 still exists")
	}
}
