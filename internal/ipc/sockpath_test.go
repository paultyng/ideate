package ipc

import (
	"testing"
)

func TestSocketPath(t *testing.T) {
	t.Parallel()

	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath() returned error: %v", err)
	}
	if path == "" {
		t.Fatal("SocketPath() returned empty string")
	}
}
