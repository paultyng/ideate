package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/paultyng/ideate/internal/app"
	"github.com/paultyng/ideate/internal/config"
	"github.com/paultyng/ideate/internal/ipc"
	"github.com/paultyng/ideate/internal/review"
	"github.com/paultyng/ideate/internal/store"
)

// formatReviewCreateError renders an error from CreateOrReopen* for CLI
// surfaces. When the error is *store.ReviewInProgressError, the message
// names the in-progress review's identifying metadata so the agent (or
// human) can decide whether the conflict is its own pending review or a
// different one. Cobra surfaces the returned error as non-zero exit.
func formatReviewCreateError(err error) error {
	var rip *store.ReviewInProgressError
	if errors.As(err, &rip) {
		var detail string
		switch rip.Kind {
		case review.KindMarkdown:
			detail = fmt.Sprintf(" kind=markdown path=%s", rip.Path)
		case review.KindDiff:
			detail = fmt.Sprintf(" kind=diff repo=%s base=%s head=%s ref=%s", rip.Repo, rip.BaseCommit, rip.HeadCommit, rip.HeadRef)
		}
		return fmt.Errorf("review already in progress: id=%s%s", rip.ID, detail)
	}
	return fmt.Errorf("creating review: %w", err)
}

// diffFlags reads --repo / --base / --head off a review-diff cmd. Returns
// values defaulted by cobra (`.` / `main` / `HEAD`).
func diffFlags(cmd *cobra.Command) (repo, base, head string) {
	repo, _ = cmd.Flags().GetString("repo")
	base, _ = cmd.Flags().GetString("base")
	head, _ = cmd.Flags().GetString("head")
	return
}

// reviewCmd is the top-level review command. With one positional arg it opens
// the review UI for that ID (kind-detected from the record). Subcommands
// `status`, `diff`, `md` cover the agent-driven flows.
var reviewCmd = &cobra.Command{
	Use:   "review [review-id]",
	Short: "Open a review by ID, or manage agent reviews",
	Long: `Open the Ideate review UI for an existing review by ID, or use a
subcommand to query status / start a new review.

Examples:
  ideate review <review-id>           Open the review UI for an existing review
  ideate review status <review-id>    Print the review record as JSON
  ideate review diff [--repo --base --head]            Open diff review UI
  ideate review diff start [--repo --base --head]     Create diff review, print ID`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return cmd.Help()
		}
		return runReviewOpenByID(cmd, args[0])
	},
}

var reviewStatusCmd = &cobra.Command{
	Use:   "status <review-id>",
	Short: "Print a review record as JSON",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReviewStatus(cmd, args[0])
	},
}

var reviewDiffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Open the diff review UI for the current branch",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runReviewDiffOpen(cmd)
	},
}

var reviewDiffStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Create a diff review and print its ID (for agent use)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runReviewDiffStart(cmd)
	},
}

var reviewMdCmd = &cobra.Command{
	Use:   "md",
	Short: "Markdown review subcommands",
}

var reviewMdStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Create a markdown review and print its ID (for agent use)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runReviewMdStart(cmd)
	},
}

func init() {
	reviewDiffCmd.Flags().String("repo", ".", "path to local git repo")
	reviewDiffCmd.Flags().String("base", "main", "base branch or ref")
	reviewDiffCmd.Flags().String("head", "HEAD", "head branch or ref")
	reviewDiffStartCmd.Flags().String("repo", ".", "path to local git repo")
	reviewDiffStartCmd.Flags().String("base", "main", "base branch or ref")
	reviewDiffStartCmd.Flags().String("head", "HEAD", "head branch or ref")
	reviewMdStartCmd.Flags().String("path", "", "path to .md or .mdx file under review (required)")

	reviewDiffCmd.AddCommand(reviewDiffStartCmd)
	reviewMdCmd.AddCommand(reviewMdStartCmd)
	reviewCmd.AddCommand(reviewStatusCmd, reviewDiffCmd, reviewMdCmd)
}

// reviewStore returns an FSStore wired up to the user's central reviews dir.
// CLI subcommands use this for direct disk access when the daemon isn't running.
func reviewStore() *store.FSStore {
	configDir := config.DefaultConfigDir()
	return store.NewFSStore("", config.ReviewsDir(configDir), "", "")
}

// runReviewOpenByID opens an existing review by ID via IPC, or launches a
// standalone app window if the daemon isn't running.
func runReviewOpenByID(cmd *cobra.Command, reviewID string) error {
	client, err := ipc.NewClient()
	if err == nil {
		if err := client.OpenReview(cmd.Context(), ipc.OpenReviewArgs{ReviewID: reviewID}); err == nil {
			return nil
		}
	}
	if flagNoGUI(cmd) {
		return fmt.Errorf("app not running (use `ideate` to start it)")
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "App not running, launching...")
	return app.Launch(app.LaunchConfig{
		View:         "review",
		Params:       map[string]string{"reviewId": reviewID},
		Standalone:   true,
		PreventSleep: flagPreventSleep(cmd),
	})
}

// runReviewStatus reads a review record from the central store and prints it
// as JSON. Works without a running daemon.
func runReviewStatus(cmd *cobra.Command, reviewID string) error {
	r, err := reviewStore().ReadReview(reviewID)
	if err != nil {
		return fmt.Errorf("reading review %q: %w", reviewID, err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling review: %w", err)
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// runReviewDiffOpen opens the diff viewer for given refs (no record creation).
// Used for browsing diffs without committing to a review.
func runReviewDiffOpen(cmd *cobra.Command) error {
	repoFlag, baseFlag, headFlag := diffFlags(cmd)
	repo, err := filepath.Abs(repoFlag)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}

	client, err := ipc.NewClient()
	if err == nil {
		if err := client.OpenReview(cmd.Context(), ipc.OpenReviewArgs{
			Repo: repo, Base: baseFlag, Head: headFlag,
		}); err == nil {
			return nil
		}
	}
	if flagNoGUI(cmd) {
		return fmt.Errorf("app not running (use `ideate` to start it)")
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "App not running, launching...")
	return app.Launch(app.LaunchConfig{
		View: "review",
		Params: map[string]string{
			"repo": repo, "base": baseFlag, "head": headFlag,
		},
		Standalone:   true,
		PreventSleep: flagPreventSleep(cmd),
	})
}

// runReviewMdStart creates a markdown review record in the central store
// and prints its ID. Tries to open the review UI in a running daemon, else
// launches standalone.
func runReviewMdStart(cmd *cobra.Command) error {
	pathFlag, _ := cmd.Flags().GetString("path")
	if pathFlag == "" {
		return fmt.Errorf("--path is required")
	}
	abs, err := filepath.Abs(pathFlag)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if ext != ".md" && ext != ".mdx" {
		return fmt.Errorf("path %q must be a .md or .mdx file", abs)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	r, _, err := reviewStore().CreateOrReopenMarkdownReview(review.MarkdownCreateOpts{
		Path:     abs,
		Original: string(content),
	})
	if err != nil {
		return formatReviewCreateError(err)
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), r.ID)

	ctx := cmd.Context()
	client, ipcErr := ipc.NewClient()
	if ipcErr == nil {
		_ = client.OpenReview(ctx, ipc.OpenReviewArgs{ReviewID: r.ID})
		return nil
	}
	if flagNoGUI(cmd) {
		return nil
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "App not running, launching...")
	return app.Launch(app.LaunchConfig{
		View:         "review",
		Params:       map[string]string{"reviewId": r.ID},
		Standalone:   true,
		PreventSleep: flagPreventSleep(cmd),
	})
}

// runReviewDiffStart creates a new diff review record in the central store
// and prints its ID. Tries to open the review UI in a running daemon, else
// launches standalone.
func runReviewDiffStart(cmd *cobra.Command) error {
	repoFlag, baseFlag, headFlag := diffFlags(cmd)
	repo, err := filepath.Abs(repoFlag)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}

	ctx := cmd.Context()
	baseSHA, err := review.ResolveRef(ctx, repo, baseFlag)
	if err != nil {
		return fmt.Errorf("resolving base ref %q: %w", baseFlag, err)
	}
	headSHA, err := review.ResolveRef(ctx, repo, headFlag)
	if err != nil {
		return fmt.Errorf("resolving head ref %q: %w", headFlag, err)
	}
	headRef := review.CurrentBranch(ctx, repo)

	r, _, err := reviewStore().CreateOrReopenDiffReview(review.CreateOpts{
		BaseCommit: baseSHA,
		HeadCommit: headSHA,
		HeadRef:    headRef,
		Repo:       repo,
	})
	if err != nil {
		return formatReviewCreateError(err)
	}

	// Print review ID to stdout — this is what the agent reads.
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), r.ID)

	client, ipcErr := ipc.NewClient()
	if ipcErr == nil {
		_ = client.OpenReview(ctx, ipc.OpenReviewArgs{
			Repo: repo, Base: baseFlag, Head: headFlag, ReviewID: r.ID,
		})
		return nil
	}
	if flagNoGUI(cmd) {
		return nil
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "App not running, launching...")
	return app.Launch(app.LaunchConfig{
		View: "review",
		Params: map[string]string{
			"repo":     repo,
			"base":     baseFlag,
			"head":     headFlag,
			"reviewId": r.ID,
		},
		Standalone:   true,
		PreventSleep: flagPreventSleep(cmd),
	})
}
