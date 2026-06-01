package app

import (
	"encoding/json"
	"os"
	"path/filepath"

	ideatecfg "github.com/paultyng/ideate/internal/config"
)

// windowState holds the persisted window position and size.
type windowState struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// windowStatePath returns the path to the window state file.
func windowStatePath() (string, error) {
	return filepath.Join(ideatecfg.DefaultConfigDir(), "window.json"), nil
}

// loadWindowState reads the persisted window state, returning nil if not found.
func loadWindowState() *windowState {
	path, err := windowStatePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var state windowState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	if state.Width < 400 || state.Height < 300 {
		return nil
	}
	return &state
}

// saveWindowState persists the window state to disk.
func saveWindowState(state windowState) {
	path, err := windowStatePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
