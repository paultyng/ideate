// Package repo wraps the small set of git worktree operations Ideate needs to
// link a canonical clone to a per-idea workspace. All operations use
// go-git v6's native API or pure-Go filesystem reads — no `git` binary
// dependency at runtime.
//
// AddWorktree decouples v6's coupled name/branch defaults using
// WithDetachedHead() — the worktree is created without v6's automatic
// branch-creation, then we open the new worktree and check out our
// real branch (which may contain slashes that the admin-name regex
// would reject). The admin name comes from filepath.Base(worktreePath),
// which is always slug-shaped at the caller and always passes the
// regex.
//
// The linked-worktree Status bugs (go-git #1843, #1896) require either a
// bare-repo backing or nested worktrees — neither matches Ideate's layout,
// so the v6 native Worktree.Status path is safe here. Reads using
// `repo.Head()` on linked worktrees are also safe: the dotgit common-dir
// resolution that v5 required via `EnableDotGitCommonDir` is now
// unconditional in PlainOpenWithOptions.
package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
	xworktree "github.com/go-git/go-git/v6/x/plumbing/worktree"
)

// openRepo opens the repo at path. Linked worktrees resolve refs against the
// canonical's object database; v6 makes this unconditional in
// PlainOpenWithOptions (the v5 EnableDotGitCommonDir toggle is gone).
func openRepo(path string) (*git.Repository, error) {
	return git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: false})
}

// Status describes a worktree's current working-tree state.
type Status struct {
	Branch string
	Dirty  bool
	Ahead  int
	Behind int
}

// AddWorktree creates a linked worktree of canonical at worktreePath checked
// out on branch. If branch already exists in canonical, it's checked out;
// otherwise it's created from current HEAD (or from the canonical's branch
// commit when branch already resolves there). branch must be non-empty —
// the caller picks the default (e.g. <prefix><slug>).
//
// Two-step under the hood: v6's worktree.Add couples the admin name and
// branch name when it auto-creates a branch, so we pass WithDetachedHead
// to skip that, then open the new worktree and do branch setup ourselves.
// This lets the branch contain characters (like `/`) that v6's admin-name
// regex would reject.
func AddWorktree(_ context.Context, canonical, worktreePath, branch string) error {
	if branch == "" {
		return errors.New("branch is required")
	}
	canonicalRepo, err := openRepo(canonical)
	if err != nil {
		return fmt.Errorf("opening canonical at %s: %w", canonical, err)
	}
	branchRefName := plumbing.NewBranchReferenceName(branch)
	var targetHash plumbing.Hash
	branchExisted := true
	if ref, err := canonicalRepo.Reference(branchRefName, false); err == nil {
		targetHash = ref.Hash()
	} else {
		branchExisted = false
		head, err := canonicalRepo.Head()
		if err != nil {
			return fmt.Errorf("resolving canonical HEAD: %w", err)
		}
		targetHash = head.Hash()
	}

	wts, err := xworktree.New(canonicalRepo.Storer)
	if err != nil {
		return fmt.Errorf("worktree manager: %w", err)
	}
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		return fmt.Errorf("creating worktree dir: %w", err)
	}
	adminName := filepath.Base(worktreePath)
	if err := wts.Add(
		osfs.New(worktreePath),
		adminName,
		xworktree.WithDetachedHead(),
		xworktree.WithCommit(targetHash),
	); err != nil {
		return fmt.Errorf("worktree add: %w", err)
	}

	wtRepo, err := openRepo(worktreePath)
	if err != nil {
		return fmt.Errorf("opening new worktree: %w", err)
	}
	wt, err := wtRepo.Worktree()
	if err != nil {
		return fmt.Errorf("opening worktree object: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: branchRefName,
		Create: !branchExisted,
	}); err != nil {
		return fmt.Errorf("checkout %q: %w", branch, err)
	}
	return nil
}

// WorktreeAdminExists reports whether canonical has a registered worktree
// admin entry under the given leaf name. Two ideas linking the same
// canonical clone would otherwise collide on `<canonical>/.git/worktrees/<name>`
// (or `<canonical>/worktrees/<name>` for a bare canonical), since git keys
// worktree admin entries by leaf basename, not by full path.
func WorktreeAdminExists(canonical, name string) bool {
	if canonical == "" || name == "" {
		return false
	}
	for _, candidate := range []string{
		filepath.Join(canonical, ".git", "worktrees", name),
		filepath.Join(canonical, "worktrees", name),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
}

// SetUpstream configures upstreamRef as the upstream of branch inside the
// worktree at worktreePath. Returns (false, nil) if upstreamRef doesn't
// resolve in the repo — the caller decides whether to log/skip; the
// worktree is still usable. upstreamRef is in `<remote>/<branch>` shape
// (e.g. "origin/main"), matching `branch --set-upstream-to`.
func SetUpstream(_ context.Context, worktreePath, upstreamRef, branch string) (bool, error) {
	r, err := openRepo(worktreePath)
	if err != nil {
		return false, fmt.Errorf("opening repo at %s: %w", worktreePath, err)
	}
	// Verify the upstream resolves. Mirrors `git rev-parse --verify --quiet`.
	if _, err := r.ResolveRevision(plumbing.Revision(upstreamRef)); err != nil {
		return false, nil
	}
	// Parse <remote>/<branch>. Strip refs/remotes/ if the caller passed the full ref.
	short := strings.TrimPrefix(upstreamRef, "refs/remotes/")
	slash := strings.Index(short, "/")
	if slash <= 0 || slash == len(short)-1 {
		return false, fmt.Errorf("upstream %q is not in <remote>/<branch> form", upstreamRef)
	}
	remote, remoteBranch := short[:slash], short[slash+1:]
	cfg, err := r.Config()
	if err != nil {
		return false, fmt.Errorf("reading config: %w", err)
	}
	if cfg.Branches == nil {
		cfg.Branches = map[string]*config.Branch{}
	}
	cfg.Branches[branch] = &config.Branch{
		Name:   branch,
		Remote: remote,
		Merge:  plumbing.NewBranchReferenceName(remoteBranch),
	}
	if err := r.SetConfig(cfg); err != nil {
		return false, fmt.Errorf("writing config: %w", err)
	}
	return true, nil
}

// RemoveWorktree removes the worktree at worktreePath. With force, the
// uncommitted-changes safety check is skipped — mirrors `git worktree remove
// --force`.
//
// v6's Remove only deletes the admin metadata at
// `<canonical>/.git/worktrees/<name>`; we delete the on-disk worktree
// directory afterward to match `git worktree remove` end-to-end.
func RemoveWorktree(ctx context.Context, worktreePath string, force bool) error {
	canonical, err := Canonical(ctx, worktreePath)
	if err != nil {
		return fmt.Errorf("resolving canonical: %w", err)
	}
	if !force {
		dirty, err := isDirty(ctx, worktreePath)
		if err != nil {
			return fmt.Errorf("checking worktree status: %w", err)
		}
		if dirty {
			return fmt.Errorf("worktree at %s has uncommitted changes; pass force=true to discard them", worktreePath)
		}
	}
	canonicalRepo, err := openRepo(canonical)
	if err != nil {
		return fmt.Errorf("opening canonical at %s: %w", canonical, err)
	}
	wts, err := xworktree.New(canonicalRepo.Storer)
	if err != nil {
		return fmt.Errorf("worktree manager: %w", err)
	}
	adminName := filepath.Base(worktreePath)
	if err := wts.Remove(adminName); err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("removing worktree dir: %w", err)
	}
	return nil
}

// ReadStatus reads the working-tree state of the worktree at worktreePath.
// Branch reports the abbreviated name (or "HEAD" in detached state). Dirty is
// true if any tracked file is modified, staged, or untracked. Ahead/Behind
// are zero when the branch has no upstream.
func ReadStatus(ctx context.Context, worktreePath string) (Status, error) {
	var s Status
	branch, err := branchName(ctx, worktreePath)
	if err != nil {
		return s, fmt.Errorf("reading branch: %w", err)
	}
	s.Branch = branch

	dirty, err := isDirty(ctx, worktreePath)
	if err != nil {
		return s, fmt.Errorf("reading status: %w", err)
	}
	s.Dirty = dirty

	ahead, behind, err := aheadBehind(ctx, worktreePath)
	if err != nil {
		return s, fmt.Errorf("reading ahead/behind: %w", err)
	}
	s.Ahead = ahead
	s.Behind = behind
	return s, nil
}

// Canonical returns the absolute path of the canonical repository that owns
// the worktree at worktreePath. Walks the `.git` entry directly rather than
// shelling to `git rev-parse --git-common-dir`. Two cases:
//
//   - `.git` is a directory → the worktree IS the canonical; return its path.
//   - `.git` is a file containing `gitdir: <path>` → linked worktree. The
//     pointed-at dir is `<canonical>/.git/worktrees/<name>`, so strip two
//     levels to recover the canonical's worktree root.
func Canonical(_ context.Context, worktreePath string) (string, error) {
	dotgit := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(dotgit)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", dotgit, err)
	}
	if info.IsDir() {
		abs, err := filepath.Abs(worktreePath)
		if err != nil {
			return "", fmt.Errorf("resolving canonical path: %w", err)
		}
		return filepath.Clean(abs), nil
	}
	f, err := os.Open(dotgit)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", dotgit, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", dotgit, err)
	}
	const prefix = "gitdir:"
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("%s: missing 'gitdir:' prefix", dotgit)
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	// gitDir is <canonical>/.git/worktrees/<name>. Two filepath.Dir calls
	// climb back to <canonical>/.git, then one more to the canonical root.
	canonical := filepath.Dir(filepath.Dir(filepath.Dir(gitDir)))
	abs, err := filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("resolving canonical path: %w", err)
	}
	return filepath.Clean(abs), nil
}

// WorktreeAdminDir returns the canonical-side admin directory for the named
// linked worktree (<canonical>/.git/worktrees/<name>). The watcher uses this
// path to detect HEAD changes (branch switches) without watching the noisy
// worktree contents.
func WorktreeAdminDir(canonical, name string) string {
	return filepath.Join(canonical, ".git", "worktrees", name)
}

// OriginURL returns the URL configured for the `origin` remote, or "" if no
// origin is set.
func OriginURL(_ context.Context, worktreePath string) (string, error) {
	r, err := openRepo(worktreePath)
	if err != nil {
		return "", fmt.Errorf("opening repo at %s: %w", worktreePath, err)
	}
	remote, err := r.Remote("origin")
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("looking up origin: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", nil
	}
	return urls[0], nil
}

// DeriveName picks a flat leaf name for a worktree given an origin URL and/or
// the canonical path. Strips a trailing .git, takes the last path segment of
// the URL, falls back to the canonical's basename if the URL is empty or
// unparseable, and slugifies the result.
func DeriveName(originURL, canonicalPath string) string {
	if name := nameFromURL(originURL); name != "" {
		return name
	}
	return slugify(filepath.Base(canonicalPath))
}

func nameFromURL(originURL string) string {
	if originURL == "" {
		return ""
	}
	cleaned := strings.TrimSuffix(strings.TrimSpace(originURL), "/")
	cleaned = strings.TrimSuffix(cleaned, ".git")
	if cleaned == "" {
		return ""
	}
	last := cleaned
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	if i := strings.LastIndex(last, ":"); i >= 0 {
		last = last[i+1:]
	}
	return slugify(last)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '_' || r == '.':
			if !dash {
				b.WriteRune('-')
				dash = true
			}
		default:
			if !dash {
				b.WriteRune('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// branchName mirrors `git rev-parse --abbrev-ref HEAD`: returns the branch
// short-name when HEAD points at a branch, or "HEAD" when detached.
func branchName(_ context.Context, dir string) (string, error) {
	r, err := openRepo(dir)
	if err != nil {
		return "", err
	}
	head, err := r.Head()
	if err != nil {
		return "", err
	}
	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}
	return "HEAD", nil
}

// isDirty reports whether the working tree has any uncommitted changes
// (modified, staged, or untracked tracked files). Mirrors `git status
// --porcelain` returning non-empty. The linked-worktree Status bugs that
// motivated the prior shell-out (go-git #1843, #1896) require a bare-repo
// backing or nested worktrees — neither matches Ideate's layout, verified
// via the probe_test.go suite.
func isDirty(_ context.Context, dir string) (bool, error) {
	r, err := openRepo(dir)
	if err != nil {
		return false, err
	}
	wt, err := r.Worktree()
	if err != nil {
		return false, err
	}
	st, err := wt.Status()
	if err != nil {
		return false, err
	}
	return !st.IsClean(), nil
}

// aheadBehind compares HEAD against its upstream. Returns (0, 0, nil) when no
// upstream is configured — typical for a fresh per-idea branch.
//
// Mirrors `git rev-list --left-right --count @{u}...HEAD`:
//   - ahead = commits reachable from HEAD but not from upstream
//   - behind = commits reachable from upstream but not from HEAD
//
// Implementation: find the merge-base of HEAD and upstream, then walk
// each side counting commits down to (but not including) that base.
func aheadBehind(_ context.Context, dir string) (int, int, error) {
	r, err := openRepo(dir)
	if err != nil {
		return 0, 0, nil
	}
	head, err := r.Head()
	if err != nil {
		return 0, 0, nil
	}
	if !head.Name().IsBranch() {
		return 0, 0, nil // detached HEAD — no upstream relationship
	}
	cfg, err := r.Config()
	if err != nil {
		return 0, 0, nil
	}
	branchCfg := cfg.Branches[head.Name().Short()]
	if branchCfg == nil || branchCfg.Remote == "" || branchCfg.Merge == "" {
		return 0, 0, nil
	}
	upstreamName := plumbing.NewRemoteReferenceName(branchCfg.Remote, branchCfg.Merge.Short())
	upstreamRef, err := r.Reference(upstreamName, true)
	if err != nil {
		return 0, 0, nil
	}
	headCommit, err := r.CommitObject(head.Hash())
	if err != nil {
		return 0, 0, fmt.Errorf("loading HEAD commit: %w", err)
	}
	upstreamCommit, err := r.CommitObject(upstreamRef.Hash())
	if err != nil {
		return 0, 0, fmt.Errorf("loading upstream commit: %w", err)
	}
	bases, err := headCommit.MergeBase(upstreamCommit)
	if err != nil {
		return 0, 0, fmt.Errorf("finding merge-base: %w", err)
	}
	if len(bases) == 0 {
		return 0, 0, nil
	}
	mb := bases[0].Hash
	ahead, err := countCommitsTo(r, head.Hash(), mb)
	if err != nil {
		return 0, 0, fmt.Errorf("counting ahead: %w", err)
	}
	behind, err := countCommitsTo(r, upstreamRef.Hash(), mb)
	if err != nil {
		return 0, 0, fmt.Errorf("counting behind: %w", err)
	}
	return ahead, behind, nil
}

// countCommitsTo walks the commit graph from `from` toward `stop`, returning
// the number of commits strictly between them (excluding `stop`). Used to
// count one side of an ahead/behind symmetric difference.
func countCommitsTo(r *git.Repository, from, stop plumbing.Hash) (int, error) {
	iter, err := r.Log(&git.LogOptions{From: from})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash == stop {
			return storer.ErrStop
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
