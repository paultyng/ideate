package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// openRepo opens the git repository at repoPath. Linked worktrees (where
// `.git` is a file pointing into the canonical repo's `worktrees/<name>`
// dir) resolve refs against the shared object database; in go-git v6 this
// is the default behavior — the v5-era `EnableDotGitCommonDir` toggle has
// been retired since the dotgit common-dir resolution is now unconditional
// in `PlainOpenWithOptions`.
func openRepo(repoPath string) (*git.Repository, error) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{
		DetectDotGit: false,
	})
	if err != nil {
		return nil, fmt.Errorf("opening repo at %s: %w", repoPath, err)
	}
	return repo, nil
}

// resolveRevision resolves a ref (branch, tag, short SHA, HEAD~3, etc.) to a
// full commit SHA in the given repo.
func resolveRevision(repo *git.Repository, ref string) (plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolving %q: %w", ref, err)
	}
	return *hash, nil
}

// commitForRef returns the commit object for a ref string.
func commitForRef(repo *git.Repository, ref string) (*object.Commit, error) {
	hash, err := resolveRevision(repo, ref)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("loading commit %s: %w", hash, err)
	}
	return commit, nil
}

// threeDotPatch computes the patch that `git diff base...head` would produce
// — i.e. changes from the merge-base of base/head to head. Returns the patch
// and the merge-base commit so callers can resolve file content at the
// "old" side without resolving the same merge-base twice.
func threeDotPatch(ctx context.Context, repo *git.Repository, base, head string) (*object.Patch, *object.Commit, error) {
	baseCommit, err := commitForRef(repo, base)
	if err != nil {
		return nil, nil, err
	}
	headCommit, err := commitForRef(repo, head)
	if err != nil {
		return nil, nil, err
	}

	// MergeBase returns possibly multiple bases; pick the first (matches
	// `git diff base...head` for the common single-base case).
	mergeBases, err := baseCommit.MergeBase(headCommit)
	if err != nil {
		return nil, nil, fmt.Errorf("finding merge-base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, nil, fmt.Errorf("no merge-base between %s and %s", base, head)
	}
	mb := mergeBases[0]

	patch, err := mb.PatchContext(ctx, headCommit)
	if err != nil {
		return nil, nil, fmt.Errorf("computing patch: %w", err)
	}
	return patch, mb, nil
}

// fileChanges returns one fileStatus per changed file in the patch, plus a
// per-file map of the unified diff text for that file.
func fileChanges(patch *object.Patch) ([]fileStatus, map[string]string, error) {
	fps := patch.FilePatches()
	statuses := make([]fileStatus, 0, len(fps))
	for _, fp := range fps {
		from, to := fp.Files()
		var status, oldPath, newPath string
		switch {
		case from == nil && to != nil:
			status = "added"
			oldPath = to.Path()
			newPath = to.Path()
		case from != nil && to == nil:
			status = "deleted"
			oldPath = from.Path()
			newPath = from.Path()
		case from != nil && to != nil && from.Path() != to.Path():
			status = "renamed"
			oldPath = from.Path()
			newPath = to.Path()
		case from != nil && to != nil:
			status = "modified"
			oldPath = from.Path()
			newPath = to.Path()
		default:
			// Both nil — shouldn't happen; skip.
			continue
		}
		statuses = append(statuses, fileStatus{
			Status:  status,
			OldPath: oldPath,
			NewPath: newPath,
		})
	}

	// Use the patch's full text + splitDiff to extract per-file unified diff
	// sections. go-git's Patch.String() emits standard `diff --git a/x b/x`
	// headers that the frontend's @git-diff-view renderer expects.
	sections := splitDiff(patch.String())
	return statuses, sections, nil
}

// blobAtRef returns the file content at a specific commit, with a size cap.
// Returns ("", false, nil) if the file does not exist at that ref (matches
// the prior behavior of `git show ref:path` failing silently).
func blobAtRef(commit *object.Commit, path string, limit int64) (string, bool, error) {
	file, err := commit.File(path)
	if err != nil {
		// Not found — caller treats as empty content.
		if errors.Is(err, object.ErrFileNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("opening %s at %s: %w", path, commit.Hash, err)
	}
	reader, err := file.Reader()
	if err != nil {
		return "", false, fmt.Errorf("reading %s at %s: %w", path, commit.Hash, err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", false, fmt.Errorf("reading %s at %s: %w", path, commit.Hash, err)
	}
	if int64(len(data)) > limit {
		return "", true, nil
	}
	return string(data), false, nil
}

// currentBranchName returns the current branch name, or empty for detached HEAD.
func currentBranchName(repo *git.Repository) string {
	head, err := repo.Head()
	if err != nil {
		return ""
	}
	if !head.Name().IsBranch() {
		return ""
	}
	return strings.TrimPrefix(string(head.Name()), "refs/heads/")
}
