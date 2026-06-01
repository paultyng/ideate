// seedtest populates the .ideate-dev ideas directory with a fixed
// manifest of demo-grade ideas spanning the dimensions a fresh user
// should see on the dashboard: status mix, resource variety, a few
// ideas with backlog items.
//
// This is test infrastructure only — there is intentionally no public
// CLI surface for idea creation; agents create ideas via MCP. Run via
// `task seed:testdata`, which sets the env vars and asserts the
// dev-sandbox path.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

func main() {
	ideasDir := os.Getenv("IDEATE_IDEAS_DIR")
	configDir := os.Getenv("IDEATE_CONFIG_DIR")
	if ideasDir == "" || configDir == "" {
		log.Fatal("IDEATE_IDEAS_DIR and IDEATE_CONFIG_DIR must be set")
	}
	if err := assertSafePath(ideasDir); err != nil {
		log.Fatalf("safety check failed: %v", err)
	}
	if err := assertSafePath(configDir); err != nil {
		log.Fatalf("safety check failed: %v", err)
	}

	if err := os.MkdirAll(ideasDir, 0o755); err != nil {
		log.Fatalf("mkdir ideas: %v", err)
	}

	st := store.NewFSStore(ideasDir, config.ReviewsDir(configDir), "", "")
	ctx := context.Background()

	for _, spec := range manifest {
		idea := &model.Idea{
			Name:      spec.Name,
			Status:    spec.Status,
			Resources: spec.Resources,
		}
		if err := st.Create(ctx, idea); err != nil {
			log.Fatalf("create %q: %v", spec.Name, err)
		}
		for _, item := range spec.Backlog {
			if _, err := st.AddBacklogItem(ctx, idea.Slug, item); err != nil {
				log.Fatalf("add backlog item to %q: %v", spec.Name, err)
			}
		}
		fmt.Printf("seeded %s (%s, %d resources, %d backlog)\n",
			idea.Slug, spec.Status, len(spec.Resources), len(spec.Backlog))
	}
	fmt.Printf("\nseeded %d ideas into %s\n", len(manifest), ideasDir)
}

// assertSafePath refuses unless the resolved path carries a recognized
// test-sandbox marker segment: ".ideate-dev" (dogfood via `task dev`) or
// "ideate-test" (per-run temp dir from `task test:ui`). Belt-and-suspenders
// on top of the Taskfile's shell guard — protects against a future caller
// that bypasses the task wrapper.
func assertSafePath(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", p, err)
	}
	sep := string(filepath.Separator)
	markers := []string{".ideate-dev", "ideate-test"}
	for _, m := range markers {
		if strings.Contains(abs, sep+m+sep) || strings.HasSuffix(abs, sep+m) {
			return nil
		}
	}
	return fmt.Errorf("path %q is not under a recognized test sandbox (.ideate-dev or ideate-test)", abs)
}

type ideaSpec struct {
	Name      string
	Status    model.Status
	Resources []model.Resource
	Backlog   []model.BacklogItem
}

// manifest is the fixed set of demo ideas. Each one is shaped to
// illustrate a different work pattern: code-heavy implementation,
// incident response, pure documentation, research with no code,
// cleanup/refactor, archived.
var manifest = []ideaSpec{
	{
		Name:   "Migrate auth service to OIDC",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/acme/identity/pull/342", Label: "identity: OIDC issuer config", Status: "open"},
			{Type: "feature_flag", URL: "https://flags.example.com/projects/identity/flags/oidc-rollout", Label: "oidc-rollout", Status: "10%"},
			{Type: "notion", URL: "https://www.notion.so/acme/OIDC-Migration-Plan-abc123", Label: "OIDC migration plan"},
		},
		Backlog: []model.BacklogItem{
			{Title: "Wire OIDC issuer config in identity service", Body: "PR #342 covers the handler + tests. Needs a follow-up to drop the legacy SAML branch.", Status: model.BacklogStatusInProgress},
			{Title: "Rollout plan: 10% → 50% → 100% gates", Body: "Coordinate with platform on the per-tenant cutover; flag oidc-rollout drives the percentage.", Status: model.BacklogStatusOpen},
		},
	},
	{
		Name:   "p99 latency regression in search",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "web", URL: "https://grafana.example.com/d/search-latency", Label: "Search latency dashboard"},
			{Type: "web", URL: "https://acme.slack.com/archives/C0SEARCH/p1748100000", Label: "#search-incident thread"},
		},
		Backlog: []model.BacklogItem{
			{Title: "Bisect: which deploy introduced the regression?", Body: "Symptom started ~2026-05-26 17:00 UTC. Last green deploy: search@a3f2c19. First red: search@b4ee8a1.", Status: model.BacklogStatusOpen},
		},
	},
	{
		Name:   "v2 API design doc",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "notion", URL: "https://www.notion.so/acme/v2-API-Design-doc-xyz789", Label: "v2 API design doc"},
			{Type: "web", URL: "https://acme.atlassian.net/wiki/spaces/ARCH/pages/12345/V2+API", Label: "Confluence: v2 API"},
		},
	},
	{
		Name:   "Evaluate vector DB for semantic search",
		Status: model.StatusPaused,
		Resources: []model.Resource{
			{Type: "notion", URL: "https://www.notion.so/acme/Vector-DB-Bake-off-def456", Label: "Vector DB bake-off notes"},
			{Type: "web", URL: "https://docs.pinecone.io/", Label: "Pinecone docs"},
			{Type: "web", URL: "https://weaviate.io/developers/weaviate", Label: "Weaviate docs"},
		},
	},
	{
		Name:   "Optimize bulk import performance",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/acme/pipeline/pull/89", Label: "pipeline: batched insert", Status: "draft"},
		},
	},
	{
		Name:   "Postmortem: 2026-05-14 checkout outage",
		Status: model.StatusPaused,
		Resources: []model.Resource{
			{Type: "notion", URL: "https://www.notion.so/acme/PM-2026-05-14-checkout-outage-ghi012", Label: "Postmortem draft"},
		},
	},
	{
		Name:   "Refactor session lifecycle",
		Status: model.StatusPaused,
	},
	{
		Name:   "Sunset old admin dashboard",
		Status: model.StatusArchived,
	},
}
