package store

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFilterFS(t *testing.T) {
	t.Parallel()

	under := fstest.MapFS{
		"keep.md":              {Data: []byte("a")},
		"drop.txt":             {Data: []byte("b")},
		"sub/keep.md":          {Data: []byte("c")},
		"sub/nested/deep.md":   {Data: []byte("d")},
		"hidden/secret.md":     {Data: []byte("e")},
		"hidden/more/other.md": {Data: []byte("f")},
	}

	// Arbitrary, ideate-independent predicate: skip anything under a
	// "hidden" segment, and any non-.md regular file.
	skip := func(p string, d fs.DirEntry) bool {
		for _, seg := range strings.Split(p, "/") {
			if seg == "hidden" {
				return true
			}
		}
		return d != nil && !d.IsDir() && !strings.HasSuffix(p, ".md")
	}

	f := newFilterFS(under, skip)

	var seen []string
	if err := fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			seen = append(seen, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	sort.Strings(seen)

	want := []string{"keep.md", "sub/keep.md", "sub/nested/deep.md"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("visible files = %v, want %v", seen, want)
	}

	// Skipped paths are invisible to direct Open and Stat too.
	for _, skipped := range []string{"drop.txt", "hidden", "hidden/secret.md", "hidden/more/other.md"} {
		if _, err := f.Open(skipped); err == nil {
			t.Errorf("Open(%q) succeeded; want ErrNotExist", skipped)
		}
		if sf, ok := f.(fs.StatFS); ok {
			if _, err := sf.Stat(skipped); err == nil {
				t.Errorf("Stat(%q) succeeded; want ErrNotExist", skipped)
			}
		}
	}

	// Skipped children never appear in their parent's ReadDir.
	rf, ok := f.(fs.ReadDirFS)
	if !ok {
		t.Fatal("filterFS is not a ReadDirFS")
	}
	entries, err := rf.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, e := range entries {
		if e.Name() == "hidden" || e.Name() == "drop.txt" {
			t.Errorf("ReadDir(.) yielded skipped entry %q", e.Name())
		}
	}
}
