package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestIndexGenerated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	alpha := &model.Idea{
		Name:        "Alpha Idea",
		Description: "The first idea about widgets.",
		Status:      model.StatusPaused,
		Body:        "Alpha body.\n",
	}
	beta := &model.Idea{
		Name:        "Beta Idea",
		Description: "The second idea about gadgets.",
		Status:      model.StatusPaused,
		Body:        "Beta body.\n",
	}
	if err := s.Create(ctx, alpha); err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	if err := s.Create(ctx, beta); err != nil {
		t.Fatalf("Create beta: %v", err)
	}

	// A non-concept sibling inside an idea must never be indexed.
	repoReadme := filepath.Join(dir, alpha.Slug, reposDir, "foo", "README.md")
	if err := os.MkdirAll(filepath.Dir(repoReadme), 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	if err := os.WriteFile(repoReadme, []byte("# repo readme\n"), 0o644); err != nil {
		t.Fatalf("write repo readme: %v", err)
	}
	sessionJSON := filepath.Join(dir, alpha.Slug, sessionsDir, "sess.json")
	if err := os.WriteFile(sessionJSON, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write session json: %v", err)
	}
	// Regenerate again so the just-added siblings would have a chance to leak.
	if err := s.regenerateIndexes(); err != nil {
		t.Fatalf("regenerateIndexes: %v", err)
	}

	// Root index.md: okf_version stamped, both ideas listed with descriptions.
	rootIdx := readFile(t, filepath.Join(dir, "index.md"))
	if !strings.Contains(rootIdx, "okf_version:") {
		t.Errorf("root index.md missing okf_version frontmatter:\n%s", rootIdx)
	}
	if !strings.Contains(rootIdx, okfBundleVersion) {
		t.Errorf("root index.md missing version %q:\n%s", okfBundleVersion, rootIdx)
	}
	for _, want := range []string{alpha.Slug, beta.Slug, alpha.Description, beta.Description} {
		if !strings.Contains(rootIdx, want) {
			t.Errorf("root index.md missing %q:\n%s", want, rootIdx)
		}
	}
	// The excluded sibling must not leak into any listing.
	if strings.Contains(rootIdx, "README") || strings.Contains(rootIdx, "repo readme") {
		t.Errorf("root index.md leaked excluded repo content:\n%s", rootIdx)
	}

	// Per-idea index.md lists that idea's idea.md concept.
	alphaIdx := readFile(t, filepath.Join(dir, alpha.Slug, indexFilename))
	if !strings.Contains(alphaIdx, "idea.md") {
		t.Errorf("alpha index.md does not list idea.md:\n%s", alphaIdx)
	}
	if !strings.Contains(alphaIdx, alpha.Name) {
		t.Errorf("alpha index.md missing idea title %q:\n%s", alpha.Name, alphaIdx)
	}
	if strings.Contains(alphaIdx, "okf_version:") {
		t.Errorf("per-idea index.md must not carry frontmatter:\n%s", alphaIdx)
	}

	// No index.md should be generated inside an excluded directory.
	if _, err := os.Stat(filepath.Join(dir, alpha.Slug, reposDir, indexFilename)); !os.IsNotExist(err) {
		t.Errorf("index.md unexpectedly generated under repos/: err=%v", err)
	}
}

// A *.md symlink inside an idea must be dropped from the bundle view: os.DirFS
// follows symlinks, so without the DirEntry symlink check the target's content
// would be ingested and rendered into index.md.
func TestBundleExcludeSkipsSymlink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Alpha Idea", Description: "First.", Body: "b\n"}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const secret = "TOPSECRET-symlink-target-content"
	secretPath := filepath.Join(dir, "secret-target.md")
	if err := os.WriteFile(secretPath, []byte("# leak\n"+secret+"\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	linkPath := filepath.Join(dir, idea.Slug, "leak.md")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if err := s.regenerateIndexes(); err != nil {
		t.Fatalf("regenerateIndexes: %v", err)
	}

	ideaIdx := readFile(t, filepath.Join(dir, idea.Slug, indexFilename))
	if strings.Contains(ideaIdx, "leak.md") || strings.Contains(ideaIdx, secret) {
		t.Errorf("index.md leaked symlink target:\n%s", ideaIdx)
	}
	rootIdx := readFile(t, filepath.Join(dir, "index.md"))
	if strings.Contains(rootIdx, secret) {
		t.Errorf("root index.md leaked symlink target:\n%s", rootIdx)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s unexpectedly exists: err=%v", path, err)
	}
}

// Dot-dirs and nested git checkouts inside an idea must never receive an
// index.md, while ordinary content subdirs still do.
func TestIndexSkipsDotDirsAndCheckouts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Alpha Idea", Description: "First.", Body: "b\n"}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	base := filepath.Join(dir, idea.Slug)
	md := "# doc\n\nbody\n"
	writeFile(t, filepath.Join(base, ".reviews", "r.md"), md)
	writeFile(t, filepath.Join(base, ".worktrees", "x", "w.md"), md)
	writeFile(t, filepath.Join(base, ".worktrees", "x", "sub", "w2.md"), md)
	writeFile(t, filepath.Join(base, "somecheckout", ".git"), "gitdir: /nowhere\n")
	writeFile(t, filepath.Join(base, "somecheckout", "c.md"), md)
	writeFile(t, filepath.Join(base, "somecheckout", "sub", "c2.md"), md)
	writeFile(t, filepath.Join(base, "notes", "n.md"), md)
	writeFile(t, filepath.Join(base, "gitdircheckout", ".git", "HEAD"), "ref: x\n")
	writeFile(t, filepath.Join(base, "gitdircheckout", "g.md"), md)

	if err := s.regenerateIndexes(); err != nil {
		t.Fatalf("regenerateIndexes: %v", err)
	}

	for _, p := range []string{
		".reviews", ".worktrees", filepath.Join(".worktrees", "x"), filepath.Join(".worktrees", "x", "sub"),
		"somecheckout", filepath.Join("somecheckout", "sub"), "gitdircheckout",
	} {
		assertNoFile(t, filepath.Join(base, p, indexFilename))
	}
	if _, err := os.Stat(filepath.Join(base, "notes", indexFilename)); err != nil {
		t.Errorf("notes/index.md missing: %v", err)
	}
	if rootIdx := readFile(t, filepath.Join(dir, indexFilename)); !strings.Contains(rootIdx, idea.Slug) {
		t.Errorf("root index.md missing idea:\n%s", rootIdx)
	}
}

// An idea dir that is itself a git repo is still an idea, not a checkout.
func TestIndexKeepsIdeaThatIsGitRepo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, dir := newTestStore(t)

	idea := &model.Idea{Name: "Repo Idea", Description: "Is a repo.", Body: "b\n"}
	if err := s.Create(ctx, idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeFile(t, filepath.Join(dir, idea.Slug, ".git", "HEAD"), "ref: x\n")
	if err := s.regenerateIndexes(); err != nil {
		t.Fatalf("regenerateIndexes: %v", err)
	}
	if rootIdx := readFile(t, filepath.Join(dir, indexFilename)); !strings.Contains(rootIdx, idea.Slug) {
		t.Errorf("root index.md dropped git-repo idea:\n%s", rootIdx)
	}
	readFile(t, filepath.Join(dir, idea.Slug, indexFilename))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
