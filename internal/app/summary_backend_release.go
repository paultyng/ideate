//go:build !dev

package app

import "github.com/paultyng/ideate/internal/agent/headless"

// pickHeadlessTestAgent returns nil in release builds — the
// testagent backend is dev-only. See summary_backend_dev.go for the
// dev override.
func pickHeadlessTestAgent() headless.Runner { return nil }
