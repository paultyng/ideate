package ipc

import (
	"testing"
)

func TestSocketPath(t *testing.T) {
	t.Setenv("IDEATE_CONFIG_DIR", t.TempDir())

	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath() returned error: %v", err)
	}
	if path == "" {
		t.Fatal("SocketPath() returned empty string")
	}
}

func TestDarwinSocketPathEmptyConfigDir(t *testing.T) {
	t.Parallel()
	if p, err := darwinSocketPath(""); err == nil {
		t.Fatalf("expected error, got %q", p)
	}
	if p, err := darwinSocketPath("/c"); err != nil || p != "/c/ideate.sock" {
		t.Fatalf("got %q, %v", p, err)
	}
}
