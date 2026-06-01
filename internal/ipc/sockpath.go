package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// SocketPath returns the platform-specific path for the IPC Unix domain socket.
func SocketPath() (string, error) {
	// IDEATE_CONFIG_DIR overrides the default — enables dev/production isolation.
	if dir := os.Getenv("IDEATE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "ideate.sock"), nil
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "ideate", "ideate.sock"), nil

	case "linux":
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Join(dir, "ideate", "ideate.sock"), nil
		}
		return filepath.Join("/tmp", "ideate-"+strconv.Itoa(os.Getuid()), "ideate.sock"), nil

	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			return "", fmt.Errorf("LOCALAPPDATA environment variable not set")
		}
		return filepath.Join(localAppData, "ideate", "ideate.sock"), nil

	default:
		return "", fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
