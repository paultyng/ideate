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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
