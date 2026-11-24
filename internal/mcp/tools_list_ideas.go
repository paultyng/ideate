package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/paultyng/ideate/internal/model"
)

// list_ideas returns an inlined per-idea state envelope so
// summarize-ideas / work-idea / dashboards can drive off a single
// MCP call. State (summary, backlog counts, session lists,
// last_activity_at) is always inlined; the only opt-in field is
// recent_output, which hits the live coordinator and so stays
// behind include_output_lines.

type ideaSummary struct {
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary,omitempty"`
	LastActivityAt string          `json:"last_activity_at,omitempty"`
	Backlog        backlogSummary  `json:"backlog"`
	Sessions       sessionsSummary `json:"sessions"`
	// URL is the ideate:// deep-link to the idea detail page.
	URL string `json:"idea_url"`
	// ActiveSessionURL routes to the running session if one exists,
	// resumes the most-recent dormant session if not, or falls
	// through to the idea page. Stable across the idea's lifetime.
	ActiveSessionURL string `json:"idea_active_session_url"`
	// RecentOutput is populated only when include_output_lines > 0
	// AND the idea has a running session. Taken from the most
	// recent running session's vscreen.
	RecentOutput string `json:"recent_output,omitempty"`
}

type backlogSummary struct {
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Done       int `json:"done"`
	WontFix    int `json:"wontfix"`
	// InProgressTitles caps at MaxBacklogTitlesInline so the
	// readout can name what's mid-flight without a second
	// list_backlog round-trip on every idea.
	InProgressTitles []string `json:"in_progress_titles,omitempty"`
}

type sessionsSummary struct {
	Running []sessionRowSummary `json:"running,omitempty"`
	Dormant []sessionRowSummary `json:"dormant,omitempty"`
}

type sessionRowSummary struct {
	UUID     string `json:"uuid"`
	Agent    string `json:"agent_type"`
	Activity string `json:"activity,omitempty"` // running only
	Started  string `json:"started"`
	Ended    string `json:"ended,omitempty"` // dormant only
	URL      string `json:"session_url"`
}

// MaxBacklogTitlesInline caps the InProgressTitles slice so a
// pathological "100 in-progress items on one idea" list doesn't
// bloat the list_ideas payload. The first N gives the recap enough
// signal; the rest can be fetched with list_backlog_by_slug if
// needed.
const MaxBacklogTitlesInline = 5

func (m *Manager) handleListIdeas(sessionID string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = sessionID

		excludeArchived := request.GetBool("exclude_archived", true)
		outputLines := request.GetInt("include_output_lines", 0)
		if outputLines < 0 {
			outputLines = 0
		}

		ideas, err := m.store.List(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("listing ideas: %v", err)), nil
		}

		summaries := make([]ideaSummary, 0, len(ideas))
		for _, idea := range ideas {
			if excludeArchived && idea.Status == model.StatusArchived {
				continue
			}
			summaries = append(summaries, m.buildIdeaSummary(ctx, idea, outputLines))
		}

		data, err := json.MarshalIndent(summaries, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshaling: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// buildIdeaSummary loads the per-idea state for one list_ideas
// entry. Every disk read is best-effort — a missing sidecar /
// missing backlog file / unreadable session record falls back to
// the empty state so one bad idea doesn't fail the whole call.
func (m *Manager) buildIdeaSummary(ctx context.Context, idea model.Idea, outputLines int) ideaSummary {
	out := ideaSummary{
		Slug:             idea.Slug,
		Name:             idea.Name,
		Status:           string(idea.Status),
		URL:              model.IdeaURL(idea.Slug),
		ActiveSessionURL: model.IdeaActiveSessionURL(idea.Slug),
	}
	if !idea.Updated.IsZero() {
		out.LastActivityAt = idea.Updated.Format(time.RFC3339Nano)
	}
	out.Summary = idea.Description

	if items, err := m.store.ListBacklog(ctx, idea.Slug); err == nil {
		for _, item := range items {
			switch item.Status {
			case model.BacklogStatusOpen:
				out.Backlog.Open++
			case model.BacklogStatusInProgress:
				out.Backlog.InProgress++
				if len(out.Backlog.InProgressTitles) < MaxBacklogTitlesInline {
					out.Backlog.InProgressTitles = append(out.Backlog.InProgressTitles, item.Title)
				}
			case model.BacklogStatusDone:
				out.Backlog.Done++
			case model.BacklogStatusWontFix:
				out.Backlog.WontFix++
			}
		}
	}

	if sessions, err := m.store.ListSessions(ctx, idea.Slug); err == nil {
		var firstRunningUUID string
		for _, s := range sessions {
			row := sessionRowSummary{
				UUID:    s.UUID,
				Agent:   s.Agent,
				Started: s.Started.Format(time.RFC3339Nano),
				URL:     model.SessionURL(idea.Slug, s.UUID),
			}
			if s.Ended != nil {
				row.Ended = s.Ended.Format(time.RFC3339Nano)
			}
			switch s.Status {
			case model.SessionStatusRunning:
				row.Activity = string(s.Activity)
				out.Sessions.Running = append(out.Sessions.Running, row)
				if firstRunningUUID == "" {
					firstRunningUUID = s.UUID
				}
			case model.SessionStatusDormant:
				out.Sessions.Dormant = append(out.Sessions.Dormant, row)
			}
		}
		// recent_output: most-recent running session only. Dormant
		// snapshot reads belong on get_session_output by uuid — the
		// list_ideas tail is a "what's the live agent showing right
		// now?" affordance, not a session-by-session replay.
		if outputLines > 0 && firstRunningUUID != "" {
			if tail, ok := m.recentOutput(firstRunningUUID, outputLines); ok {
				out.RecentOutput = tail
			}
		}
	}

	return out
}
