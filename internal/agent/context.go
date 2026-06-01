package agent

import "os"

// cleanupTempFiles removes the given file paths, ignoring errors.
func cleanupTempFiles(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}
