package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/paultyng/ideate/internal/agent/transcript/claudefmt"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/store"
)

// errStatusOnUpdateMsg is surfaced when a caller passes `status` to
// update_idea / update_idea_by_slug. Status transitions are explicit
// lifecycle tools (archive_idea, unarchive_idea, pause_idea, resume_idea)
// — passing the field directly to update_idea bypasses the side effects
// each transition carries (archive releases repos, etc.).
const errStatusOnUpdateMsg = "status is not accepted by update_idea; use archive_idea, unarchive_idea, pause_idea, or resume_idea"

// formatReviewCreateError renders an error from CreateOrReopen* into a tool
// result. When the error is *store.ReviewInProgressError, the response
// names the in-progress review's identifying metadata (kind + path or
// kind + repo/refs) so the agent can decide whether the conflict is its
// own pending review (poll the existing ID) or someone else's (wait,
// retry, or surface to the user). Any other error is reported verbatim.
func formatReviewCreateError(err error) *mcp.CallToolResult {
	var rip *store.ReviewInProgressError
	if errors.As(err, &rip) {
		var detail string
		switch rip.Kind {
		case review.KindMarkdown:
			detail = fmt.Sprintf("kind=markdown path=%s", rip.Path)
		case review.KindDiff:
			detail = fmt.Sprintf("kind=diff repo=%s base=%s head=%s ref=%s", rip.Repo, rip.BaseCommit, rip.HeadCommit, rip.HeadRef)
		default:
			detail = fmt.Sprintf("kind=%s", rip.Kind)
		}
		msg := fmt.Sprintf(
			"review already in progress (id=%s %s). If this matches the review you intended to request, poll get_%s_review_result with this id; otherwise wait for it to complete or surface to the user.",
			rip.ID, detail, rip.Kind,
		)
		return mcp.NewToolResultError(msg)
	}
	return mcp.NewToolResultError(fmt.Sprintf("creating review: %v", err))
}

// --- Tool definitions ---

func getIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_idea",
		Description: desc("get_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}
}

func listResourcesTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_resources",
		Description: desc("list_resources"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}
}

func addResourceTool() mcp.Tool {
	return mcp.Tool{
		Name:        "add_resource",
		Description: desc("add_resource"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"type": map[string]any{
					"type":        "string",
					"description": "Resource type (e.g. github_pr, notion, jira, feature_flag, datadog)",
				},
				"url": map[string]any{
					"type":        "string",
					"description": "URL of the resource",
				},
				"label": map[string]any{
					"type":        "string",
					"description": "Display label for the resource",
				},
			},
			Required: []string{"type"},
		},
	}
}

func deleteResourceTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_resource",
		Description: desc("delete_resource"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "URL of the resource to remove",
				},
			},
			Required: []string{"url"},
		},
	}
}

func updateIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "update_idea",
		Description: desc("update_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Updated idea name",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "Updated idea summary/description (markdown)",
				},
			},
		},
	}
}

// --- Tool handlers ---

func (m *Manager) handleGetIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idea, _, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Return a structured view of the idea.
		view := map[string]any{
			"name":      idea.Name,
			"slug":      idea.Slug,
			"status":    idea.Status,
			"created":   idea.Created,
			"updated":   idea.Updated,
			"resources": idea.Resources,
		}
		if idea.Summary != "" {
			view["summary"] = idea.Summary
		}

		data, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling idea: %v", err)), nil
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) resolveIdea(ctx context.Context, sessionID string) (*model.Idea, string, error) {
	slug, err := m.resolver.GetIdeaSlug(sessionID)
	if err != nil {
		return nil, "", fmt.Errorf("resolving idea for session %s: %w", sessionID, err)
	}
	idea, err := m.store.Get(ctx, slug)
	if err != nil {
		return nil, slug, fmt.Errorf("loading idea %s: %w", slug, err)
	}
	return idea, slug, nil
}

func (m *Manager) handleListResources(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idea, _, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, err := json.MarshalIndent(idea.Resources, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling resources: %v", err)), nil
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleAddResource(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		resource := model.Resource{
			Type:  request.GetString("type", ""),
			URL:   request.GetString("url", ""),
			Label: request.GetString("label", ""),
		}

		if err := m.store.AddResource(ctx, slug, resource); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("adding resource: %v", err)), nil
		}

		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "resource_added",
			Session:   sessionID,
			Fields: map[string]any{
				"resource_type": resource.Type,
				"label":         resource.Label,
			},
		})

		m.emit(EventResourceAdded, map[string]any{
			"slug":     slug,
			"resource": resource,
		})

		return mcp.NewToolResultText(fmt.Sprintf("Added %s resource: %s", resource.Type, resource.Label)), nil
	}
}

func (m *Manager) handleDeleteResource(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url := request.GetString("url", "")
		if url == "" {
			return mcp.NewToolResultError("url is required"), nil
		}

		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		deleted, err := m.store.DeleteResource(ctx, slug, url)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("deleting resource: %v", err)), nil
		}

		if deleted {
			_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
				Timestamp: time.Now(),
				Event:     "resource_deleted",
				Session:   sessionID,
				Fields:    map[string]any{"url": url},
			})
			m.emit(EventResourceDeleted, map[string]any{"slug": slug, "url": url})
			return mcp.NewToolResultText("Deleted resource: " + url), nil
		}
		return mcp.NewToolResultText("No resource with URL " + url + " (no-op)"), nil
	}
}

func (m *Manager) handleUpdateIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Reject status field: status transitions are explicit lifecycle tools.
		if _, present := optionalString(request.GetArguments(), "status"); present {
			return mcp.NewToolResultError(errStatusOnUpdateMsg), nil
		}

		idea, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		changes := []string{}

		if s := request.GetString("name", ""); s != "" {
			idea.Name = s
			changes = append(changes, "name="+s)
		}
		if s := request.GetString("summary", ""); s != "" {
			idea.Summary = s
			changes = append(changes, "summary updated")
		}

		if len(changes) == 0 {
			return mcp.NewToolResultText("No changes specified"), nil
		}

		if err := m.store.Update(ctx, idea); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("updating idea: %v", err)), nil
		}

		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "idea_updated",
			Session:   sessionID,
			Fields:    map[string]any{"changes": changes},
		})

		m.emit(EventIdeaUpdated, map[string]any{
			"slug":    slug,
			"changes": changes,
		})

		return mcp.NewToolResultText(fmt.Sprintf("Updated idea: %s", strings.Join(changes, ", "))), nil
	}
}

// --- Review tools ---

func requestDiffReviewTool() mcp.Tool {
	return mcp.Tool{
		Name:        "request_diff_review",
		Description: desc("request_diff_review"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Absolute path to the git repository",
				},
				"base": map[string]any{
					"type":        "string",
					"description": "Base git ref (branch, tag, SHA, HEAD~N, etc.)",
				},
				"head": map[string]any{
					"type":        "string",
					"description": "Head git ref (branch, tag, SHA, HEAD, etc.)",
				},
			},
			Required: []string{"repo", "base", "head"},
		},
	}
}

func getDiffReviewResultTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_diff_review_result",
		Description: desc("get_diff_review_result"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"review_id": map[string]any{
					"type":        "string",
					"description": "Review ID returned by request_diff_review",
				},
			},
			Required: []string{"review_id"},
		},
	}
}

func (m *Manager) handleRequestDiffReview(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repoArg := request.GetString("repo", "")
		base := request.GetString("base", "")
		head := request.GetString("head", "")

		if repoArg == "" || base == "" || head == "" {
			return mcp.NewToolResultError("repo, base, and head are required"), nil
		}

		slug, err := m.resolver.GetIdeaSlug(sessionID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving idea for session: %v", err)), nil
		}
		repoPath, err := m.store.ResolveRepoPath(ctx, slug, repoArg)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Resolve refs to full commit SHAs.
		baseSHA, err := review.ResolveRef(ctx, repoPath, base)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving base ref: %v", err)), nil
		}
		headSHA, err := review.ResolveRef(ctx, repoPath, head)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving head ref: %v", err)), nil
		}

		headRef := review.CurrentBranch(ctx, repoPath)

		r, _, err := m.store.CreateOrReopenDiffReview(review.CreateOpts{
			BaseCommit: baseSHA,
			HeadCommit: headSHA,
			HeadRef:    headRef,
			Repo:       repoPath,
			SessionID:  sessionID,
			IdeaSlug:   slug,
		})
		if err != nil {
			return formatReviewCreateError(err), nil
		}

		m.markSessionReviewing(ctx, sessionID, slug, r.ID)

		// Emit event to open review UI.
		m.emit(EventReviewCreated, map[string]any{
			"review_id": r.ID,
			"repo":      repoPath,
			"base":      baseSHA,
			"head":      headSHA,
		})
		// Coarse "pending reviews list changed" signal for views
		// like the topbar bar that don't care about navigation.
		m.emit(EventReviewChanged, map[string]any{
			"review_id": r.ID,
			"status":    string(r.Status),
		})

		result := map[string]any{
			"review_id": r.ID,
			"status":    string(r.Status),
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleGetDiffReviewResult(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewID := request.GetString("review_id", "")
		if reviewID == "" {
			return mcp.NewToolResultError("review_id is required"), nil
		}

		// Register a waiter BEFORE reading status — otherwise NotifyReviewComplete
		// could fire in the gap between read and register, leaving us blocked
		// for the full 60s while the review is already done on disk.
		ch, cleanup := m.waitForReview(reviewID)
		defer cleanup()

		r, err := m.store.ReadReview(reviewID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading review: %v", err)), nil
		}
		if r.Status != review.ReviewPending {
			m.clearSessionReviewing(ctx, sessionID, r.IdeaSlug)
			return reviewResultText(r), nil
		}

		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()

		select {
		case <-ch:
			r, err = m.store.ReadReview(reviewID)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reading review: %v", err)), nil
			}
			m.clearSessionReviewing(ctx, sessionID, r.IdeaSlug)
			return reviewResultText(r), nil
		case <-timer.C:
			return reviewResultText(r), nil
		case <-ctx.Done():
			return reviewResultText(r), nil
		}
	}
}

func reviewResultText(r *review.Review) *mcp.CallToolResult {
	data, _ := json.MarshalIndent(r, "", "  ")
	return mcp.NewToolResultText(string(data))
}

// --- Markdown review tools ---

func requestMarkdownReviewTool() mcp.Tool {
	return mcp.Tool{
		Name:        "request_markdown_review",
		Description: desc("request_markdown_review"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the markdown file under review (.md or .mdx)",
				},
			},
			Required: []string{"path"},
		},
	}
}

func getMarkdownReviewResultTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_markdown_review_result",
		Description: desc("get_markdown_review_result"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"review_id": map[string]any{
					"type":        "string",
					"description": "Review ID returned by request_markdown_review",
				},
			},
			Required: []string{"review_id"},
		},
	}
}

func (m *Manager) handleRequestMarkdownReview(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pathArg := request.GetString("path", "")
		if pathArg == "" {
			return mcp.NewToolResultError("path is required"), nil
		}

		absPath, err := filepath.Abs(pathArg)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving path: %v", err)), nil
		}
		ext := strings.ToLower(filepath.Ext(absPath))
		if ext != ".md" && ext != ".mdx" {
			return mcp.NewToolResultError(fmt.Sprintf("path %q must be a .md or .mdx file", absPath)), nil
		}

		content, err := os.ReadFile(absPath) //nolint:gosec // agent-supplied path is trusted in this local-first app
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading file: %v", err)), nil
		}

		// Idea slug is best-effort: set if the session has one, leave empty otherwise.
		ideaSlug, _ := m.resolver.GetIdeaSlug(sessionID)

		r, _, err := m.store.CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
			Path:      absPath,
			Original:  string(content),
			SessionID: sessionID,
			IdeaSlug:  ideaSlug,
		})
		if err != nil {
			return formatReviewCreateError(err), nil
		}

		m.markSessionReviewing(ctx, sessionID, ideaSlug, r.ID)

		m.emit(EventReviewCreated, map[string]any{
			"review_id": r.ID,
			"kind":      string(r.Kind),
			"path":      absPath,
		})
		m.emit(EventReviewChanged, map[string]any{
			"review_id": r.ID,
			"status":    string(r.Status),
		})

		result := map[string]any{
			"review_id": r.ID,
			"status":    string(r.Status),
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleGetMarkdownReviewResult(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewID := request.GetString("review_id", "")
		if reviewID == "" {
			return mcp.NewToolResultError("review_id is required"), nil
		}

		ch, cleanup := m.waitForReview(reviewID)
		defer cleanup()

		r, err := m.store.ReadReview(reviewID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading review: %v", err)), nil
		}
		if r.Status != review.ReviewPending {
			m.clearSessionReviewing(ctx, sessionID, r.IdeaSlug)
			return reviewResultText(r), nil
		}

		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()

		select {
		case <-ch:
			r, err = m.store.ReadReview(reviewID)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reading review: %v", err)), nil
			}
			m.clearSessionReviewing(ctx, sessionID, r.IdeaSlug)
			return reviewResultText(r), nil
		case <-timer.C:
			return reviewResultText(r), nil
		case <-ctx.Done():
			return reviewResultText(r), nil
		}
	}
}

// --- Cancel review ---

func cancelReviewTool() mcp.Tool {
	return mcp.Tool{
		Name:        "cancel_review",
		Description: desc("cancel_review"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"review_id": map[string]any{
					"type":        "string",
					"description": "Review ID returned by request_diff_review or request_markdown_review.",
				},
			},
			Required: []string{"review_id"},
		},
	}
}

func (m *Manager) handleCancelReview(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewID := request.GetString("review_id", "")
		if reviewID == "" {
			return mcp.NewToolResultError("review_id is required"), nil
		}
		r, err := m.store.ReadReview(reviewID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reading review: %v", err)), nil
		}
		// A session can only cancel a review it owns. Sessionless (CLI) reviews
		// have Session=="" and aren't cancellable from MCP.
		if r.Session != sessionID {
			return mcp.NewToolResultError("review is not owned by the calling session"), nil
		}
		if r.Status != review.ReviewPending {
			return reviewResultText(r), nil
		}
		cancelled, err := m.store.CancelReview(reviewID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("cancelling review: %v", err)), nil
		}
		m.clearSessionReviewing(ctx, sessionID, r.IdeaSlug)
		m.NotifyReviewComplete(reviewID, cancelled.Status)
		return reviewResultText(cancelled), nil
	}
}

// markSessionReviewing flips the calling session's record to
// Activity=reviewing + ActiveReviewID=reviewID. Best-effort: anything goes
// wrong, the review still proceeds — the UI just won't show the reviewing
// indicator until the next polling pass.
func (m *Manager) markSessionReviewing(ctx context.Context, uuid, slug, reviewID string) {
	if uuid == "" || slug == "" {
		return
	}
	if err := m.store.SetSessionReviewActive(ctx, slug, uuid, reviewID); err != nil {
		slog.Warn("setting session reviewing state",
			slog.String("slug", slug), slog.String("uuid", uuid),
			slog.String("review", reviewID), slog.Any("err", err))
	}
}

// clearSessionReviewing is the inverse — fired on cancel and on
// get_*_review_result when the review reaches a terminal status.
func (m *Manager) clearSessionReviewing(ctx context.Context, uuid, slug string) {
	if uuid == "" || slug == "" {
		return
	}
	if err := m.store.ClearSessionReview(ctx, slug, uuid); err != nil {
		slog.Warn("clearing session reviewing state",
			slog.String("slug", slug), slog.String("uuid", uuid), slog.Any("err", err))
	}
}

// --- Repo tools ---

func linkRepoTool() mcp.Tool {
	return mcp.Tool{
		Name:        "link_repo",
		Description: desc("link_repo"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the canonical repository on disk.",
				},
				"branch": map[string]any{
					"type":        "string",
					"description": "Branch to check out into the new worktree. If empty, uses the per-idea default. If the branch exists, it's checked out; otherwise it's created from current HEAD.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Override the auto-derived leaf name (use only on collisions).",
				},
			},
			Required: []string{"path"},
		},
	}
}

func listReposTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_repos",
		Description: desc("list_repos"),
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
	}
}

func unlinkRepoTool() mcp.Tool {
	return mcp.Tool{
		Name:        "unlink_repo",
		Description: desc("unlink_repo"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Worktree leaf name (as returned by list_repos).",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Skip the dirty-tree safety check.",
				},
			},
			Required: []string{"name"},
		},
	}
}

func (m *Manager) handleLinkRepo(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		path := request.GetString("path", "")
		if path == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving path: %v", err)), nil
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf("path %q is not a directory", path)), nil
		}

		name, err := m.store.LinkRepo(ctx, slug, abs, request.GetString("branch", ""), request.GetString("name", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("linking repo: %v", err)), nil
		}

		m.emit(EventRepoChanged, map[string]any{"slug": slug, "name": name})

		return mcp.NewToolResultText(fmt.Sprintf("Linked repo as %q (worktree under <idea>/repos/%s)", name, name)), nil
	}
}

func (m *Manager) handleListRepos(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		repos, err := m.store.ListRepos(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("listing repos: %v", err)), nil
		}

		data, err := json.MarshalIndent(repos, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling repos: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleUnlinkRepo(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, slug, err := m.resolveIdea(ctx, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		name := request.GetString("name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		force := request.GetBool("force", false)
		if err := m.store.UnlinkRepo(ctx, slug, name, force); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("unlinking repo: %v", err)), nil
		}

		m.emit(EventRepoChanged, map[string]any{"slug": slug, "name": name})

		return mcp.NewToolResultText(fmt.Sprintf("Unlinked repo %q", name)), nil
	}
}

// --- Cross-idea (by-slug) tools ---
//
// These take an explicit slug rather than relying on the session-bound idea.
// Used by the root orchestrator session and (future) the standalone `ideate
// mcp` entrypoint.

func listIdeasTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_ideas",
		Description: desc("list_ideas"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"exclude_archived": map[string]any{
					"type":        "boolean",
					"description": "If true (default), drop archived ideas from the result. Pass false to include them.",
				},
				"include_output_lines": map[string]any{
					"type":        "integer",
					"description": "If > 0, each idea with a running session inlines `recent_output` — the last N lines of the most recent running session's vscreen. Defaults to 0 (no inline output). Lets summarize-ideas pull live activity context in one round-trip instead of N follow-up get_session_output calls.",
				},
			},
		},
	}
}

func createIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "create_idea",
		Description: desc("create_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Display name of the idea (required).",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []any{"active", "paused", "archived"},
					"description": "Initial status. Defaults to paused (first session start auto-flips to active).",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "Optional initial summary/body for idea.md.",
				},
			},
			Required: []string{"name"},
		},
	}
}

func deleteIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_idea",
		Description: desc("delete_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the idea to delete (required).",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "If true, delete despite dirty linked worktrees. Default false.",
				},
			},
			Required: []string{"slug"},
		},
	}
}

func renameIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "rename_idea",
		Description: desc("rename_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Current slug of the idea (required).",
				},
				"new_slug": map[string]any{
					"type":        "string",
					"description": "Target slug. Must be slug-shaped (lowercase, hyphens, no spaces) and not already taken.",
				},
			},
			Required: []string{"slug", "new_slug"},
		},
	}
}

func getIdeaBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_idea_by_slug",
		Description: desc("get_idea_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the idea (as returned by list_ideas or create_idea).",
				},
			},
			Required: []string{"slug"},
		},
	}
}

// optionalString reads a string field from the raw arguments map.
// Returns (value, present=true) when the key is present AND its
// value is a string (including the empty string), so callers can
// distinguish "absent / null" (no action) from "" (explicit clear).
// CallToolRequest.GetString collapses those two cases via its
// default-value fallback, which is wrong for partial-update
// semantics.
func optionalString(args map[string]any, key string) (string, bool) {
	raw, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// optionalStringSlice returns ([]string, true) when key is present
// (even with an empty array — explicit clear), or (nil, false) when
// absent. Non-string elements are dropped silently rather than
// erroring; the alternative is fragile against agents that occasionally
// pass numbers or null in arrays.
func optionalStringSlice(args map[string]any, key string) ([]string, bool) {
	raw, ok := args[key]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

func updateIdeaBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "update_idea_by_slug",
		Description: desc("update_idea_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":    map[string]any{"type": "string", "description": "Slug of the idea."},
				"name":    map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
			},
			Required: []string{"slug"},
		},
	}
}

func addResourceBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "add_resource_by_slug",
		Description: desc("add_resource_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":  map[string]any{"type": "string"},
				"type":  map[string]any{"type": "string", "description": "Resource type (e.g. github_pr, notion, jira)."},
				"url":   map[string]any{"type": "string"},
				"label": map[string]any{"type": "string"},
			},
			Required: []string{"slug", "type"},
		},
	}
}

func deleteResourceBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_resource_by_slug",
		Description: desc("delete_resource_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{"type": "string", "description": "Slug of the idea."},
				"url":  map[string]any{"type": "string", "description": "URL of the resource to remove."},
			},
			Required: []string{"slug", "url"},
		},
	}
}

func listResourcesBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_resources_by_slug",
		Description: desc("list_resources_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{"type": "string"},
			},
			Required: []string{"slug"},
		},
	}
}

func (m *Manager) handleCreateIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		name := request.GetString("name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		status := request.GetString("status", string(model.StatusPaused))
		switch model.Status(status) {
		case model.StatusActive, model.StatusPaused, model.StatusArchived:
			// valid
		default:
			return mcp.NewToolResultError(fmt.Sprintf(
				"invalid status %q; allowed values: active, paused, archived", status)), nil
		}
		idea := &model.Idea{
			Name:    name,
			Status:  model.Status(status),
			Summary: request.GetString("summary", ""),
		}
		if err := m.store.Create(ctx, idea); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("creating idea: %v", err)), nil
		}
		m.emit(EventIdeaCreated, map[string]any{"slug": idea.Slug, "name": idea.Name})

		return mcp.NewToolResultText(idea.Slug), nil
	}
}

func (m *Manager) handleDeleteIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		force := request.GetBool("force", false)

		// Block on running sessions — same reasoning as rename_idea:
		// destructive bookkeeping while a live agent's PTY cwd is the
		// idea's tree leaves the agent flailing. Stop it first.
		sessions, err := m.store.ListSessions(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("listing sessions: %v", err)), nil
		}
		for _, sess := range sessions {
			if sess.Status == model.SessionStatusRunning {
				return mcp.NewToolResultError(fmt.Sprintf(
					"idea %q has a running session (%s); stop it before deleting",
					slug, sess.UUID,
				)), nil
			}
		}

		if err := m.store.Delete(ctx, slug, force); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete: %v", err)), nil
		}

		m.emit(EventIdeaDeleted, map[string]any{"slug": slug})
		return mcp.NewToolResultText(slug), nil
	}
}

func (m *Manager) handleRenameIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		oldSlug := request.GetString("slug", "")
		newSlug := request.GetString("new_slug", "")
		if oldSlug == "" || newSlug == "" {
			return mcp.NewToolResultError("slug and new_slug are required"), nil
		}

		result, err := m.store.RenameIdea(ctx, oldSlug, newSlug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("rename: %v", err)), nil
		}

		// Migrate Claude transcript dirs for any session whose
		// WorkingDir moved. Encoded path scheme is "/" → "-", so a
		// single os.Rename per (oldDir, newDir) pair handles every
		// session uuid that lived under that cwd.
		m.mu.RLock()
		projectsDir := m.claudeProjectsDir
		m.mu.RUnlock()
		movedTranscripts := 0
		if projectsDir != "" {
			seen := make(map[string]struct{})
			for _, mv := range result.WorkingDirMoves {
				key := mv.OldDir + "->" + mv.NewDir
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				oldEnc := claudefmt.EncodeProjectDir(mv.OldDir)
				newEnc := claudefmt.EncodeProjectDir(mv.NewDir)
				if oldEnc == newEnc {
					continue
				}
				from := filepath.Join(projectsDir, oldEnc)
				to := filepath.Join(projectsDir, newEnc)
				if _, statErr := os.Stat(from); statErr != nil {
					continue
				}
				if _, statErr := os.Stat(to); statErr == nil {
					slog.Warn("rename: claude projects dir collision; skipping",
						slog.String("from", from), slog.String("to", to))
					continue
				}
				if mvErr := os.Rename(from, to); mvErr != nil {
					slog.Warn("rename: moving claude transcript dir",
						slog.String("from", from), slog.String("to", to),
						slog.Any("err", mvErr))
					continue
				}
				movedTranscripts++
			}
		}

		m.emit(EventIdeaRenamed, map[string]any{
			"old_slug":          result.OldSlug,
			"new_slug":          result.NewSlug,
			"sessions_rewired":  result.SessionsRewired,
			"worktrees_rebuilt": result.WorktreesRebuilt,
			"dirty_worktrees":   result.DirtyWorktrees,
			"transcripts_moved": movedTranscripts,
		})

		return mcp.NewToolResultText(result.NewSlug), nil
	}
}

func (m *Manager) handleGetIdeaBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		idea, err := m.store.Get(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("getting idea: %v", err)), nil
		}
		data, err := json.MarshalIndent(idea, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

func (m *Manager) handleUpdateIdeaBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Reject status field: status transitions are explicit lifecycle tools.
		args := request.GetArguments()
		if _, present := optionalString(args, "status"); present {
			return mcp.NewToolResultError(errStatusOnUpdateMsg), nil
		}

		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		idea, err := m.store.Get(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("getting idea: %v", err)), nil
		}

		// Distinguish absent/null (no action) from "" (explicit clear)
		// by inspecting the raw arguments map directly. The library's
		// GetString collapses both to "" via the default-value fallback,
		// which made "clear the summary" impossible.
		var changes []string
		if v, present := optionalString(args, "name"); present {
			idea.Name = v
			changes = append(changes, "name="+v)
		}
		if v, present := optionalString(args, "summary"); present {
			idea.Summary = v
			if v == "" {
				changes = append(changes, "summary cleared")
			} else {
				changes = append(changes, "summary updated")
			}
		}
		if len(changes) == 0 {
			return mcp.NewToolResultText("No changes specified"), nil
		}

		if err := m.store.Update(ctx, idea); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("updating idea: %v", err)), nil
		}

		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "idea_updated",
			Session:   sessionID,
			Fields:    map[string]any{"changes": changes},
		})
		m.emit(EventIdeaUpdated, map[string]any{"slug": slug, "changes": changes})

		return mcp.NewToolResultText(fmt.Sprintf("Updated idea %s: %s", slug, strings.Join(changes, ", "))), nil
	}
}

func (m *Manager) handleAddResourceBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug := request.GetString("slug", "")
		typ := request.GetString("type", "")
		if slug == "" || typ == "" {
			return mcp.NewToolResultError("slug and type are required"), nil
		}
		resource := model.Resource{
			Type:  typ,
			URL:   request.GetString("url", ""),
			Label: request.GetString("label", ""),
		}
		if err := m.store.AddResource(ctx, slug, resource); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("adding resource: %v", err)), nil
		}
		_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
			Timestamp: time.Now(),
			Event:     "resource_added",
			Session:   sessionID,
			Fields:    map[string]any{"resource_type": resource.Type, "label": resource.Label},
		})
		m.emit(EventResourceAdded, map[string]any{"slug": slug, "resource": resource})

		return mcp.NewToolResultText(fmt.Sprintf("Added %s resource to %s: %s", resource.Type, slug, resource.Label)), nil
	}
}

func (m *Manager) handleDeleteResourceBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		url := request.GetString("url", "")
		if slug == "" || url == "" {
			return mcp.NewToolResultError("slug and url are required"), nil
		}

		deleted, err := m.store.DeleteResource(ctx, slug, url)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("deleting resource: %v", err)), nil
		}

		if deleted {
			_ = m.store.AppendHistory(ctx, slug, model.HistoryEvent{
				Timestamp: time.Now(),
				Event:     "resource_deleted",
				Session:   sessionID,
				Fields:    map[string]any{"url": url},
			})
			m.emit(EventResourceDeleted, map[string]any{"slug": slug, "url": url})
			return mcp.NewToolResultText("Deleted resource: " + url), nil
		}
		return mcp.NewToolResultText("No resource with URL " + url + " (no-op)"), nil
	}
}

func (m *Manager) handleListResourcesBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}
		idea, err := m.store.Get(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("getting idea: %v", err)), nil
		}
		data, err := json.MarshalIndent(idea.Resources, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- Lifecycle tools ---

func archiveIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "archive_idea",
		Description: desc("archive_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the idea to archive. If omitted, uses the current session's idea.",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "If true, archive despite dirty linked worktrees. Default false.",
				},
			},
		},
	}
}

func unarchiveIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "unarchive_idea",
		Description: desc("unarchive_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the archived idea to restore (required).",
				},
			},
			Required: []string{"slug"},
		},
	}
}

func pauseIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "pause_idea",
		Description: desc("pause_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the idea to pause. If omitted, uses the current session's idea.",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "Optional ISO 8601 timestamp when the pause should auto-lift.",
				},
			},
		},
	}
}

func resumeIdeaTool() mcp.Tool {
	return mcp.Tool{
		Name:        "resume_idea",
		Description: desc("resume_idea"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug": map[string]any{
					"type":        "string",
					"description": "Slug of the paused idea to resume. If omitted, uses the current session's idea.",
				},
			},
		},
	}
}

// resolveSlug returns the slug from the request arg "slug" if present,
// otherwise resolves from the session. Callers that are orchestrator-only
// pass sessionID="" and require the slug arg.
func (m *Manager) resolveSlug(ctx context.Context, sessionID string, request mcp.CallToolRequest) (string, error) {
	if slug := request.GetString("slug", ""); slug != "" {
		return slug, nil
	}
	if sessionID == "" {
		return "", fmt.Errorf("slug is required")
	}
	_, slug, err := m.resolveIdea(ctx, sessionID)
	return slug, err
}

func (m *Manager) handleArchiveIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug, err := m.resolveSlug(ctx, sessionID, request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		force := request.GetBool("force", false)

		report, err := m.store.Archive(ctx, slug, force)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("archive: %v", err)), nil
		}

		m.emit(EventIdeaUpdated, map[string]any{"slug": slug, "status": "archived"})
		return mcp.NewToolResultText(fmt.Sprintf(
			"Archived %s (released %d repos)", slug, len(report.ReleasedRepos),
		)), nil
	}
}

func (m *Manager) handleUnarchiveIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		if slug == "" {
			return mcp.NewToolResultError("slug is required"), nil
		}

		report, err := m.store.Unarchive(ctx, slug)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("unarchive: %v", err)), nil
		}

		m.emit(EventIdeaUpdated, map[string]any{"slug": slug, "status": "active"})

		msg := fmt.Sprintf("Unarchived %s", slug)
		if len(report.RepoResources) > 0 {
			msg += fmt.Sprintf("; re-link %d repo resource(s) with link_repo", len(report.RepoResources))
		}
		return mcp.NewToolResultText(msg), nil
	}
}

func (m *Manager) handlePauseIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug, err := m.resolveSlug(ctx, sessionID, request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var until *time.Time
		if s := request.GetString("until", ""); s != "" {
			t, parseErr := time.Parse(time.RFC3339, s)
			if parseErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("parsing until: %v", parseErr)), nil
			}
			until = &t
		}

		if err := m.store.Pause(ctx, slug, until); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("pause: %v", err)), nil
		}

		m.emit(EventIdeaUpdated, map[string]any{"slug": slug, "status": "paused"})

		msg := fmt.Sprintf("Paused %s", slug)
		if until != nil {
			msg += fmt.Sprintf(" until %s", until.Format(time.RFC3339))
		}
		return mcp.NewToolResultText(msg), nil
	}
}

func (m *Manager) handleResumeIdea(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slug, err := m.resolveSlug(ctx, sessionID, request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if err := m.store.Resume(ctx, slug); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resume: %v", err)), nil
		}

		m.emit(EventIdeaUpdated, map[string]any{"slug": slug, "status": "active"})
		return mcp.NewToolResultText(fmt.Sprintf("Resumed %s", slug)), nil
	}
}

// --- Cross-idea (by-slug) repo lifecycle tools ---
//
// Orchestrator-driven repo lifecycle that the session-scoped link_repo /
// unlink_repo can't reach because they derive slug from the session. The
// unarchive flow needs these (unarchive_idea returns repo URLs; the
// orchestrator re-links them without spawning an idea session); manual
// cleanup uses them too.

func linkRepoBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "link_repo_by_slug",
		Description: desc("link_repo_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":   map[string]any{"type": "string", "description": "Slug of the idea to link the repo to."},
				"path":   map[string]any{"type": "string", "description": "Absolute path to the canonical repository on disk."},
				"branch": map[string]any{"type": "string", "description": "Branch to check out (defaults to the per-idea default)."},
				"name":   map[string]any{"type": "string", "description": "Override the auto-derived leaf name (use only on collisions)."},
			},
			Required: []string{"slug", "path"},
		},
	}
}

func unlinkRepoBySlugTool() mcp.Tool {
	return mcp.Tool{
		Name:        "unlink_repo_by_slug",
		Description: desc("unlink_repo_by_slug"),
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"slug":  map[string]any{"type": "string", "description": "Slug of the idea owning the worktree."},
				"name":  map[string]any{"type": "string", "description": "Worktree leaf name (as returned by list_repos)."},
				"force": map[string]any{"type": "boolean", "description": "Skip the dirty-tree safety check."},
			},
			Required: []string{"slug", "name"},
		},
	}
}

func (m *Manager) handleLinkRepoBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		path := request.GetString("path", "")
		if slug == "" || path == "" {
			return mcp.NewToolResultError("slug and path are required"), nil
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolving path: %v", err)), nil
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf("path %q is not a directory", path)), nil
		}

		name, err := m.store.LinkRepo(ctx, slug, abs, request.GetString("branch", ""), request.GetString("name", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("linking repo: %v", err)), nil
		}

		m.emit(EventRepoChanged, map[string]any{"slug": slug, "name": name})
		return mcp.NewToolResultText(fmt.Sprintf("Linked repo as %q on idea %s", name, slug)), nil
	}
}

func (m *Manager) handleUnlinkRepoBySlug(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID
		slug := request.GetString("slug", "")
		name := request.GetString("name", "")
		if slug == "" || name == "" {
			return mcp.NewToolResultError("slug and name are required"), nil
		}

		force := request.GetBool("force", false)
		if err := m.store.UnlinkRepo(ctx, slug, name, force); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("unlinking repo: %v", err)), nil
		}

		m.emit(EventRepoChanged, map[string]any{"slug": slug, "name": name})
		return mcp.NewToolResultText(fmt.Sprintf("Unlinked repo %q from idea %s", name, slug)), nil
	}
}
