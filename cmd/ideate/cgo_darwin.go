//go:build darwin

package main

// Wails v2 doesn't pull in UniformTypeIdentifiers.framework on its own,
// but newer macOS SDKs require it for UTType symbols pulled through
// Cocoa/AppKit. Without this, `go run`/`go build` against this main
// fail with "Undefined symbols ... _OBJC_CLASS_$_UTType". `wails build`
// has its own framework wiring that already covers this; this pragma
// is for raw go invocations (task cli, task seed:testdata, tests).

// #cgo darwin LDFLAGS: -framework UniformTypeIdentifiers
import "C"
