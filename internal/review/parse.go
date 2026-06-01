package review

import (
	"strings"
)

type fileStatus struct {
	Status  string
	OldPath string
	NewPath string
}

// splitDiff splits a full unified diff into per-file sections.
// Each returned entry maps a file path to its diff content (from "diff --git" through the hunks).
func splitDiff(fullDiff string) map[string]string {
	result := make(map[string]string)
	if fullDiff == "" {
		return result
	}

	const prefix = "diff --git a/"

	sections := splitOnPrefix(fullDiff, prefix)
	for _, section := range sections {
		// Re-add the prefix that was used to split.
		section = prefix + section

		// Extract new path from "diff --git a/... b/..." line.
		firstLine := section[:strings.IndexByte(section, '\n')]
		newPath := extractNewPath(firstLine)
		if newPath != "" {
			result[newPath] = section
		}
	}
	return result
}

// splitOnPrefix splits s into sections, each starting at an occurrence of prefix.
// Text before the first occurrence is discarded. Each section runs from one
// prefix to the next (or end of string).
func splitOnPrefix(s, prefix string) []string {
	var starts []int
	offset := 0
	for {
		idx := strings.Index(s[offset:], prefix)
		if idx == -1 {
			break
		}
		starts = append(starts, offset+idx)
		offset += idx + len(prefix)
	}

	parts := make([]string, len(starts))
	for i, start := range starts {
		end := len(s)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		// Return without the prefix (caller re-adds it).
		parts[i] = s[start+len(prefix) : end]
	}
	return parts
}

// extractNewPath extracts the "b/..." path from a "diff --git a/... b/..." line.
func extractNewPath(line string) string {
	// Format: "diff --git a/old b/new"
	// Find the last " b/" which separates old from new.
	idx := strings.LastIndex(line, " b/")
	if idx == -1 {
		return ""
	}
	return line[idx+3:]
}
