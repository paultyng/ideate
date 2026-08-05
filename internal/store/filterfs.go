package store

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// filterFS is a generic read-only middleware over an underlying fs.FS: it
// delegates every operation to the underlying tree but hides any path the
// skip predicate rejects. It knows nothing about any specific domain — the
// exclusion policy lives entirely in the predicate the caller supplies.
//
// It implements fs.FS + fs.ReadDirFS + fs.StatFS. Consumers that walk with
// fs.WalkDir (which prefers ReadDirFS) never descend into or observe a
// skipped path because ReadDir drops it; Open/Stat also apply the predicate
// so a direct access to a skipped path reports fs.ErrNotExist.
type filterFS struct {
	under fs.FS
	skip  func(path string, d fs.DirEntry) bool
}

// newFilterFS wraps under with a skip predicate. under should be a
// fs.ReadDirFS (e.g. os.DirFS) so ReadDir/WalkDir delegate efficiently; the
// predicate reports true for any bundle-relative path to hide.
func newFilterFS(under fs.FS, skip func(path string, d fs.DirEntry) bool) fs.FS {
	return &filterFS{under: under, skip: skip}
}

// Open implements fs.FS. Skipped paths report fs.ErrNotExist so callers
// cannot reach past the filter by opening a known path directly.
func (f *filterFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name != "." {
		info, err := fs.Stat(f.under, name)
		if err != nil {
			return nil, err
		}
		if f.skip(name, fs.FileInfoToDirEntry(info)) {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}
	return f.under.Open(name)
}

// ReadDir implements fs.ReadDirFS, dropping skipped children. This is the
// filter fs.WalkDir honors: a skipped directory is never descended into and
// a skipped file is never yielded to the walk.
func (f *filterFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	entries, err := fs.ReadDir(f.under, name)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, e := range entries {
		child := e.Name()
		if name != "." {
			child = path.Join(name, e.Name())
		}
		if f.skip(child, e) {
			continue
		}
		kept = append(kept, e)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name() < kept[j].Name() })
	return kept, nil
}

// Stat implements fs.StatFS. Skipped paths report fs.ErrNotExist.
func (f *filterFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	info, err := fs.Stat(f.under, name)
	if err != nil {
		return nil, err
	}
	if name != "." && f.skip(name, fs.FileInfoToDirEntry(info)) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return info, nil
}

// Compile-time interface checks.
var (
	_ fs.FS        = (*filterFS)(nil)
	_ fs.ReadDirFS = (*filterFS)(nil)
	_ fs.StatFS    = (*filterFS)(nil)
)

// bundleExclude is the skip predicate that reduces the ideas directory to
// its OKF-concept view. The ideas directory is a single OKF bundle whose
// concepts are the per-idea idea.md/context/*.md files plus reserved
// index.md; the same tree also holds git worktrees (repos/), session JSON
// (sessions/), backups (.backups/), and root JSON/JSONL sidecars
// (config.json, history.jsonl, backlog.json). go-okf's Load
// must see none of that, so bundleExclude skips any path with a segment in
// the excluded-directory set, plus any regular file that is not a *.md
// concept. d may be nil; the segment rule needs only the path.
func bundleExclude(p string, d fs.DirEntry) bool {
	excludedDirs := map[string]bool{
		"repos":    true,
		"sessions": true,
		".backups": true,
	}
	for _, seg := range strings.Split(p, "/") {
		if excludedDirs[seg] {
			return true
		}
	}
	if d != nil {
		// Reject symlinks: os.DirFS follows them, so a *.md symlink whose
		// target sits outside the bundle (in repos/, another idea, or off
		// the tree entirely) would pull that target's content into the OKF
		// view and into index.md. DirEntry.Type reflects the lstat mode
		// (ReadDir/WalkDir don't follow), so the symlink is dropped at
		// discovery and never opened.
		if d.Type()&fs.ModeSymlink != 0 {
			return true
		}
		if !d.IsDir() && !strings.HasSuffix(p, ".md") {
			return true
		}
	}
	return false
}
