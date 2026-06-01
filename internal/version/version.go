// Package version exposes the build-time version string.
//
// Version is set via -ldflags at build time:
//
//	-ldflags "-X github.com/paultyng/ideate/internal/version.Version=$(git describe --tags --always --dirty)"
//
// Defaults to "dev" for `go run`, `wails dev`, and tests.
package version

var Version = "dev"
