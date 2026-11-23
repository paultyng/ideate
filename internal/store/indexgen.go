package store

import (
	"fmt"
	"os"
	"path/filepath"

	okf "github.com/paultyng/go-okf"
	"github.com/paultyng/ideate/internal/atomicfile"
)

// okfBundleVersion is stamped into the bundle-root index.md frontmatter
// (`okf_version`), the sole place frontmatter is permitted in an index.md
// (OKF v0.2 §12). It doubles as the migration gate for the bundle.
const okfBundleVersion = "0.2"

// indexFilename is the reserved OKF directory-listing file (v0.2 §8).
const indexFilename = "index.md"

// regenerateIndexes rebuilds every index.md in the ideas bundle: the
// bundle-root listing (one entry per idea) and a per-idea listing (that
// idea's concepts). It loads the bundle through the filterFS/bundleExclude
// view so go-okf only sees concepts (idea.md, context/*.md) and never
// repos/, sessions/, or the root JSON sidecars, then persists each
// regenerated index.md atomically.
//
// The bundle-root index.md is the only one carrying frontmatter; its
// okf_version is stamped to okfBundleVersion. A nil Synthesizer is passed:
// a lone-child directory (an idea whose only concept is idea.md) reuses that
// concept's own description, so the root still lists ideas with descriptions
// without any per-directory summary synthesis for M1.
//
// Perf: this reloads and re-renders the entire bundle on every idea write.
// For M1's idea counts that is fine; a per-directory dirty-cache is a later
// item on the OKF milestone plan.
func (s *FSStore) regenerateIndexes() error {
	// Whole-bundle rebuild: serialize across all slugs so two concurrent
	// idea writes don't lost-update each other's index.md snapshot. The
	// per-slug lock the caller holds is not enough — every write rewrites
	// every index.md.
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	b, err := okf.Load(newFilterFS(os.DirFS(s.ideasDir), bundleExclude))
	if err != nil {
		return fmt.Errorf("loading okf bundle: %w", err)
	}

	for dir, data := range b.RegenerateIndexes(nil) {
		if dir == "" {
			// RegenerateIndexes never sets OKFVersion; stamp it on the
			// bundle root by reparsing and re-rendering.
			if idx, perr := okf.ParseIndex(data); perr == nil {
				idx.OKFVersion = okfBundleVersion
				data = idx.Bytes()
			}
		}
		target := filepath.Join(s.ideasDir, filepath.FromSlash(dir), indexFilename)
		if err := atomicfile.Write(target, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", target, err)
		}
	}
	return nil
}
