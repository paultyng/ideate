package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func sessionsDir(configDir string) string {
	return filepath.Join(configDir, "sessions")
}

func manifestPath(configDir, id string) string {
	return filepath.Join(sessionsDir(configDir), id+".json")
}

// writeManifest persists a session manifest to disk.
func writeManifest(configDir string, m SessionManifest) error {
	dir := sessionsDir(configDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating sessions dir: %w", err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	// Match the 0o700 dir mode: the manifest carries the session UUID,
	// which is the auth token for the hooks endpoint. World-readable would
	// let another local process replay events against our hooks server.
	if err := os.WriteFile(manifestPath(configDir, m.ID), data, 0o600); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}

// removeManifest deletes a session manifest from disk.
func removeManifest(configDir, id string) error {
	if err := os.Remove(manifestPath(configDir, id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing manifest: %w", err)
	}
	return nil
}

// scanManifests reads all session manifests from the config directory.
func scanManifests(configDir string) ([]SessionManifest, error) {
	dir := sessionsDir(configDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading sessions dir: %w", err)
	}

	var manifests []SessionManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m SessionManifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// cleanStaleManifests removes manifests whose processes are no longer running.
func cleanStaleManifests(configDir string) error {
	manifests, err := scanManifests(configDir)
	if err != nil {
		return err
	}

	for _, m := range manifests {
		if err := syscall.Kill(m.PID, 0); err != nil {
			// Process does not exist — remove stale manifest.
			_ = removeManifest(configDir, m.ID)
		}
	}
	return nil
}
