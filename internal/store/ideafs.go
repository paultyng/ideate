package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// IdeaFS provides an fs.FS, fs.ReadDirFS, and fs.ReadFileFS view
// over an idea's directory. Useful for agent tools and standard
// library consumers that accept fs.FS.
type IdeaFS struct {
	dir string // absolute path to the idea directory
}

// NewIdeaFS creates an fs.FS rooted at the idea's directory.
func (s *FSStore) NewIdeaFS(slug string) *IdeaFS {
	return &IdeaFS{dir: filepath.Join(s.ideasDir, slug)}
}

// Open implements fs.FS.
func (f *IdeaFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.Open(filepath.Join(f.dir, name))
}

// ReadDir implements fs.ReadDirFS.
func (f *IdeaFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return os.ReadDir(filepath.Join(f.dir, name))
}

// ReadFile implements fs.ReadFileFS.
func (f *IdeaFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}
	return os.ReadFile(filepath.Join(f.dir, name))
}

// Stat implements fs.StatFS.
func (f *IdeaFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	return os.Stat(filepath.Join(f.dir, name))
}

// ModTime returns the modification time of the idea directory itself.
func (f *IdeaFS) ModTime() time.Time {
	info, err := os.Stat(f.dir)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// Compile-time interface checks.
var (
	_ fs.FS         = (*IdeaFS)(nil)
	_ fs.ReadDirFS  = (*IdeaFS)(nil)
	_ fs.ReadFileFS = (*IdeaFS)(nil)
	_ fs.StatFS     = (*IdeaFS)(nil)
)
