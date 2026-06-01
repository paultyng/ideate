// Package skills owns Ideate's canonical orchestrator-facing skill
// bundle. The skills ship embedded in the binary; on app start they are
// auto-installed into <ideasDir>/.claude/skills/<name>/SKILL.md if
// missing. User edits stick: auto-install never overwrites. The
// reset_default_skill MCP tool blows away the on-disk copy and rewrites
// from canonical when the user asks for it.
package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed canonical/*/SKILL.md
var canonicalFS embed.FS

// Skill is one canonical skill in the bundle.
type Skill struct {
	Name     string // matches the canonical directory name
	contents []byte // SKILL.md bytes from the embed
	sha256   string // hex sha256 of contents, computed once at load
}

// Status reports the on-disk state of a skill relative to canonical.
type Status string

const (
	StatusMissing  Status = "missing"    // file does not exist
	StatusUpToDate Status = "up-to-date" // sha matches canonical
	StatusModified Status = "modified"   // sha differs from canonical
)

// InstalledSkill is the public view of an on-disk skill.
type InstalledSkill struct {
	Name         string `json:"name"`
	Status       Status `json:"status"`
	Path         string `json:"path"`
	CanonicalSHA string `json:"canonical_sha256"`
	OnDiskSHA    string `json:"on_disk_sha256,omitempty"`
}

// canonical caches the loaded bundle keyed by skill name. Populated on
// first call to List() / Get(). Embedded reads are cheap but caching
// the sha computation isn't.
var canonical = func() map[string]*Skill {
	m := map[string]*Skill{}
	entries, err := canonicalFS.ReadDir("canonical")
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		bytes, err := canonicalFS.ReadFile(filepath.Join("canonical", name, "SKILL.md"))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(bytes)
		m[name] = &Skill{
			Name:     name,
			contents: bytes,
			sha256:   hex.EncodeToString(sum[:]),
		}
	}
	return m
}()

// Names returns the canonical skill names, sorted.
func Names() []string {
	names := make([]string, 0, len(canonical))
	for n := range canonical {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Has reports whether name is a canonical skill.
func Has(name string) bool {
	_, ok := canonical[name]
	return ok
}

// installDir returns <ideasDir>/.claude/skills.
func installDir(ideasDir string) string {
	return filepath.Join(ideasDir, ".claude", "skills")
}

// skillDir returns <ideasDir>/.claude/skills/<name>.
func skillDir(ideasDir, name string) string {
	return filepath.Join(installDir(ideasDir), name)
}

// skillFile returns <ideasDir>/.claude/skills/<name>/SKILL.md.
func skillFile(ideasDir, name string) string {
	return filepath.Join(skillDir(ideasDir, name), "SKILL.md")
}

// InstallMissing writes any canonical skill that doesn't already exist
// on disk. Existing files are left alone; user edits stick. Returns
// the names that were installed.
func InstallMissing(ideasDir string) ([]string, error) {
	if ideasDir == "" {
		return nil, fmt.Errorf("ideasDir is empty")
	}
	if err := os.MkdirAll(installDir(ideasDir), 0o755); err != nil {
		return nil, fmt.Errorf("creating skills dir: %w", err)
	}
	var installed []string
	for _, name := range Names() {
		path := skillFile(ideasDir, name)
		if _, err := os.Stat(path); err == nil {
			continue // present, leave it alone
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("creating canonical skill dir",
				slog.String("skill", name), slog.Any("err", err))
			continue
		}
		if err := os.WriteFile(path, canonical[name].contents, 0o644); err != nil {
			slog.Warn("writing canonical skill",
				slog.String("skill", name), slog.Any("err", err))
			continue
		}
		installed = append(installed, name)
	}
	return installed, nil
}

// List returns every canonical skill with its current on-disk status.
func List(ideasDir string) []InstalledSkill {
	out := make([]InstalledSkill, 0, len(canonical))
	for _, name := range Names() {
		s := canonical[name]
		entry := InstalledSkill{
			Name:         name,
			Path:         skillFile(ideasDir, name),
			CanonicalSHA: s.sha256,
		}
		bytes, err := os.ReadFile(entry.Path)
		switch {
		case err != nil && os.IsNotExist(err):
			entry.Status = StatusMissing
		case err != nil:
			// Other read errors: surface as modified-with-empty-sha so
			// the caller knows something is off without us inventing a
			// new status.
			entry.Status = StatusModified
		default:
			sum := sha256.Sum256(bytes)
			onDisk := hex.EncodeToString(sum[:])
			entry.OnDiskSHA = onDisk
			if onDisk == s.sha256 {
				entry.Status = StatusUpToDate
			} else {
				entry.Status = StatusModified
			}
		}
		out = append(out, entry)
	}
	return out
}

// Reset blows away the on-disk skill dir and rewrites it from canonical.
// If name is empty, every canonical skill is reset. Returns the names
// that were rewritten and the paths touched.
func Reset(ideasDir, name string) ([]string, error) {
	if ideasDir == "" {
		return nil, fmt.Errorf("ideasDir is empty")
	}
	var targets []string
	if name == "" {
		targets = Names()
	} else {
		if !Has(name) {
			return nil, fmt.Errorf("unknown canonical skill %q", name)
		}
		targets = []string{name}
	}
	var done []string
	for _, n := range targets {
		dir := skillDir(ideasDir, n)
		if err := os.RemoveAll(dir); err != nil {
			return done, fmt.Errorf("removing %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return done, fmt.Errorf("recreating %s: %w", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), canonical[n].contents, 0o644); err != nil {
			return done, fmt.Errorf("writing %s SKILL.md: %w", n, err)
		}
		done = append(done, n)
	}
	return done, nil
}

// CanonicalFS exposes the embedded filesystem rooted at the canonical/
// directory. Test affordance — production code uses the higher-level
// helpers above.
func CanonicalFS() fs.FS {
	sub, _ := fs.Sub(canonicalFS, "canonical")
	return sub
}

// CanonicalNamesString returns the canonical skill names joined with
// commas. Used in tool descriptions / log lines where the set is small.
func CanonicalNamesString() string {
	return strings.Join(Names(), ", ")
}
