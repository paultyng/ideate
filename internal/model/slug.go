package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	slugDateTimeRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-(\d{4})-(.+)$`)
	slugDateRe     = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-(.+)$`)

	nonAlnumHyphen = regexp.MustCompile(`[^a-z0-9-]`)
	multiHyphen    = regexp.MustCompile(`-{2,}`)

	// validSlugRe matches the slug shape Slugify produces:
	// lowercase ASCII alphanumerics + interior hyphens, starting with
	// alphanumeric. Used at boundaries that accept agent- or URL-
	// supplied slugs (e.g. ResolveActiveSession via ideate:// links)
	// to reject path-traversal segments like `..` or `foo/bar`.
	validSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// IsValidSlug reports whether s has the on-disk slug shape (lowercase
// alphanumerics + hyphens, no path separators, no leading hyphen).
// Use at boundaries that receive untrusted-source slugs (URL parsers,
// MCP arguments, deep links) before passing the value to filesystem
// operations. Internal Go callers can skip this — slugs minted by
// Slugify / GenerateSlug always satisfy it.
func IsValidSlug(s string) bool {
	return validSlugRe.MatchString(s)
}

// ParseSlug extracts an optional created-time and human name from a
// slug. Date-prefixed slugs (the legacy default) yield a real time;
// bare slugs yield (zero, slug, nil) so callers fall through to the
// idea record's own created field.
//
//	"2026-04-21-batch-processing"      → (2026-04-21 00:00 UTC, "batch-processing", nil)
//	"2026-04-21-1858-batch-processing" → (2026-04-21 18:58 UTC, "batch-processing", nil)
//	"my-idea"                          → (zero,                 "my-idea",          nil)
//	""                                 → (zero,                 "",                 error)
func ParseSlug(slug string) (created time.Time, name string, err error) {
	if slug == "" {
		return time.Time{}, "", fmt.Errorf("empty slug")
	}

	// Try date+time first: YYYY-MM-DD-HHMM-rest
	if m := slugDateTimeRe.FindStringSubmatch(slug); m != nil {
		t, err := time.Parse("2006-01-02-1504", m[1]+"-"+m[2])
		if err == nil {
			return t, m[3], nil
		}
	}

	// Try date only: YYYY-MM-DD-rest
	if m := slugDateRe.FindStringSubmatch(slug); m != nil {
		t, err := time.Parse("2006-01-02", m[1])
		if err == nil {
			return t, m[2], nil
		}
	}

	// Bare slug: no embedded timestamp. The caller is responsible for
	// reading the authoritative `created` from the idea record's
	// frontmatter (List/Get already do this for date-prefixed slugs;
	// after this change they fall back when the parsed time is zero).
	return time.Time{}, slug, nil
}

// GenerateSlug creates a date-prefixed slug from a name and timestamp.
func GenerateSlug(name string, t time.Time, includeTime bool) string {
	s := Slugify(name)
	if includeTime {
		return fmt.Sprintf("%s-%s-%s", t.Format("2006-01-02"), t.Format("1504"), s)
	}
	return fmt.Sprintf("%s-%s", t.Format("2006-01-02"), s)
}

// Slugify converts a name to a URL-safe slug.
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, s)
	s = nonAlnumHyphen.ReplaceAllString(s, "")
	s = multiHyphen.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
