package review

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/go-git/go-git/v6/plumbing/object"
)

// Diff size caps. The renderer (`@git-diff-view`) has no built-in OOM
// protection; large diffs serialize fully across the Wails bridge and into the
// React state tree, so we bound them here.
const (
	MaxFileBytes  = 1 << 20  // per-file content cap (1 MiB)
	MaxTotalBytes = 10 << 20 // raw `git diff` output cap (10 MiB)
	MaxFiles      = 500      // file count cap
)

// ErrDiffTooLarge is returned when a diff exceeds the configured caps.
// The error message includes which limit was hit and by how much so the
// frontend can render an actionable banner.
var ErrDiffTooLarge = errors.New("diff exceeds size limit")

// LocalSource implements DiffSource using local git commands.
type LocalSource struct {
	RepoPath string
	Base     string
	Head     string
}

func (s *LocalSource) GetDiff(ctx context.Context) (*DiffResult, error) {
	repo, err := openRepo(s.RepoPath)
	if err != nil {
		return nil, err
	}

	patch, mergeBase, err := threeDotPatch(ctx, repo, s.Base, s.Head)
	if err != nil {
		return nil, err
	}

	files, sections, err := fileChanges(patch)
	if err != nil {
		return nil, err
	}
	if len(files) > MaxFiles {
		return nil, fmt.Errorf("%w: %d files exceeds limit of %d",
			ErrDiffTooLarge, len(files), MaxFiles)
	}

	headCommit, err := commitForRef(repo, s.Head)
	if err != nil {
		return nil, err
	}

	var fileDiffs []FileDiff
	totalBytes := 0
	for _, f := range files {
		oldContent, oldTrunc, err := loadContent(mergeBase, f.OldPath, f.Status, "deleted", "modified", "renamed")
		if err != nil {
			return nil, err
		}
		newContent, newTrunc, err := loadContent(headCommit, f.NewPath, f.Status, "added", "modified", "renamed")
		if err != nil {
			return nil, err
		}

		hunks := sections[f.NewPath]
		totalBytes += len(hunks)
		if totalBytes > MaxTotalBytes {
			return nil, fmt.Errorf("%w: diff output exceeds %d bytes",
				ErrDiffTooLarge, MaxTotalBytes)
		}

		fileDiffs = append(fileDiffs, FileDiff{
			OldName:    f.OldPath,
			NewName:    f.NewPath,
			Status:     f.Status,
			Hunks:      hunks,
			OldContent: oldContent,
			NewContent: newContent,
			Language:   detectLanguage(f.NewPath),
			Truncated:  oldTrunc || newTrunc,
		})
	}

	return &DiffResult{
		Files: fileDiffs,
		Base:  s.Base,
		Head:  s.Head,
	}, nil
}

// loadContent fetches file contents at a given commit, but only when the
// file's status is one of the relevant statuses. Returns the content (or
// empty if the content was over MaxFileBytes) and a truncation flag.
func loadContent(commit *object.Commit, path, status string, want ...string) (string, bool, error) {
	if !slices.Contains(want, status) {
		return "", false, nil
	}
	content, truncated, err := blobAtRef(commit, path, MaxFileBytes)
	if err != nil {
		return "", false, fmt.Errorf("showing %s at %s: %w", path, commit.Hash, err)
	}
	if truncated {
		return "", true, nil
	}
	return content, false, nil
}
