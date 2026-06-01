package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kind discriminates review subtypes. Diff reviews carry git refs and inline
// file:line comments. Markdown reviews carry a path to a prose file and an
// inline-marked-up version (CriticMarkup) submitted by the human.
type Kind string

const (
	KindDiff     Kind = "diff"
	KindMarkdown Kind = "markdown"
)

// ReviewStatus represents the lifecycle state of a review.
type ReviewStatus string

const (
	ReviewPending   ReviewStatus = "pending"
	ReviewComplete  ReviewStatus = "complete"
	ReviewCancelled ReviewStatus = "cancelled"
)

// Signal is the broker payload for review-completion notifications.
// Carries the review ID + the new status so subscribers can branch
// on submitted-vs-cancelled without re-reading the on-disk record.
// Today the long-poll consumer in `internal/mcp` only matches on ID
// and re-reads anyway, but the wider type means future consumers
// (e.g. cancel-only notification on the topbar) get the status for
// free without a payload-shape migration.
type Signal struct {
	ID     string
	Status ReviewStatus
}

// ReviewEvent matches GitHub's PR review event vocabulary. Reused for markdown
// reviews so the agent gets a consistent signal across kinds.
type ReviewEvent string

const (
	EventApprove        ReviewEvent = "APPROVE"
	EventRequestChanges ReviewEvent = "REQUEST_CHANGES"
	EventComment        ReviewEvent = "COMMENT"
)

// validEvents is the set of accepted Submit events.
var validEvents = map[ReviewEvent]bool{
	EventApprove:        true,
	EventRequestChanges: true,
	EventComment:        true,
}

// ValidEvent reports whether s is an accepted review event.
func ValidEvent(s string) bool { return validEvents[ReviewEvent(s)] }

// Review is the persisted record for a human review request, regardless of
// kind. Diff- and markdown-specific payloads are carried in their respective
// optional fields and the Kind discriminator selects which one applies.
type Review struct {
	ID        string       `json:"id"`
	Kind      Kind         `json:"kind"`
	Status    ReviewStatus `json:"status"`
	Created   time.Time    `json:"created"`
	Completed *time.Time   `json:"completed,omitempty"`
	Session   string       `json:"session,omitempty"`   // agent session ID, if any
	IdeaSlug  string       `json:"idea_slug,omitempty"` // optional, set when an idea context is known

	// Common review feedback. Body is the human's overall summary text. Event
	// is APPROVE / REQUEST_CHANGES / COMMENT.
	Body  string `json:"body,omitempty"`
	Event string `json:"event,omitempty"`

	// Diff-only fields. Populated when Kind == KindDiff.
	Repo       string          `json:"repo,omitempty"`
	BaseCommit string          `json:"base_commit,omitempty"`
	HeadCommit string          `json:"head_commit,omitempty"`
	HeadRef    string          `json:"head_ref,omitempty"`
	Comments   []ReviewComment `json:"comments,omitempty"`

	// Markdown-only fields. Populated when Kind == KindMarkdown.
	Markdown *MarkdownPayload `json:"markdown,omitempty"`

	// Draft fields capture the human's in-progress edits while Status is
	// still pending. Persisted incrementally (debounced autosave from the
	// frontend) so closing the app mid-review doesn't lose work — on next
	// open, the editor hydrates from these instead of the agent's snapshot.
	// Cleared on submit so the submitted Body/Comments/Markdown.MarkedUp
	// are the authoritative record. Only meaningful while Status==pending.
	DraftBody     string          `json:"draft_body,omitempty"`     // both kinds
	DraftComments []ReviewComment `json:"draft_comments,omitempty"` // diff only
}

// ReviewComment is a single inline comment on a diff review, matching GitHub's
// PR review comment fields.
type ReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine *int   `json:"start_line,omitempty"`
	Side      string `json:"side,omitempty"`       // LEFT or RIGHT
	StartSide string `json:"start_side,omitempty"` // LEFT or RIGHT
	Body      string `json:"body"`
}

// MarkdownPayload carries markdown-review-specific data: the path to the file
// under review (relative to the agent's working dir), the original content
// snapshot at request time, the human-submitted version (which may contain
// CriticMarkup marks), and a parsed view of the human's *new* marks (any
// CriticMarkup syntax already present in Original is filtered out so doc
// literals don't appear as user edits).
type MarkdownPayload struct {
	Path     string       `json:"path"`
	Original string       `json:"original,omitempty"`
	MarkedUp string       `json:"marked_up,omitempty"`
	Marks    []CriticMark `json:"marks,omitempty"`

	// DraftMarkedUp is the in-progress CriticMarkup-edited body while
	// Status==pending. See Review.DraftBody / DraftComments for the full
	// draft persistence story. Cleared on submit.
	DraftMarkedUp string `json:"draft_marked_up,omitempty"`
}

// CriticMarkType discriminates the four supported CriticMarkup mark kinds.
type CriticMarkType string

const (
	CriticInsertion    CriticMarkType = "insertion"
	CriticDeletion     CriticMarkType = "deletion"
	CriticSubstitution CriticMarkType = "substitution"
	CriticComment      CriticMarkType = "comment"
)

// CriticMark is one parsed CriticMarkup mark. Start and Length are byte
// offsets/lengths into the source string the mark was parsed from
// (including delimiters), handy for slicing the literal back out or
// splicing replacements in. When attached to a Review's
// `MarkdownPayload.Marks` field, those positions are into MarkedUp.
//
// Per type:
//   - insertion / deletion / comment: Text holds the inner content
//   - substitution: Old / New hold the two payloads of `{~~old~>new~~}`
type CriticMark struct {
	Type   CriticMarkType `json:"type"`
	Start  int            `json:"start"`
	Length int            `json:"length"`
	Text   string         `json:"text,omitempty"`
	Old    string         `json:"old,omitempty"`
	New    string         `json:"new,omitempty"`
}

// NewCriticMarks returns the marks present in markedUp that aren't already
// in original — i.e. the human's actual edits, with documentation literals
// in the source filtered out. Pairing is by `(type, content)` multiset,
// not by position: each mark in markedUp consumes one matching mark in
// original; the leftovers are user intent.
//
// Why content-based: positions shift when the human inserts or deletes
// text, so position-correlation between original and markedUp is brittle.
// Multiset matching by content handles the common case ("docs contain
// {--del--} as an example, user didn't add new ones"). It does miss the
// edge case where the user intentionally adds a duplicate of an existing
// literal — that one gets eaten by the baseline. Acceptable for v1; the
// raw `marked_up` field is still available for consumers that need exact
// reconstruction.
func NewCriticMarks(original, markedUp string) []CriticMark {
	baseline := ParseCriticMarks(original)
	if len(baseline) == 0 {
		return ParseCriticMarks(markedUp)
	}
	counts := make(map[string]int, len(baseline))
	for _, m := range baseline {
		counts[markFingerprint(m)]++
	}
	all := ParseCriticMarks(markedUp)
	out := make([]CriticMark, 0, len(all))
	for _, m := range all {
		k := markFingerprint(m)
		if counts[k] > 0 {
			counts[k]--
			continue
		}
		out = append(out, m)
	}
	return out
}

// markFingerprint produces a multiset key for a CriticMark. Uses NUL as
// the field separator so the key is unambiguous regardless of payload
// content (NUL can't appear in either Text or Old/New — they came from a
// markdown source that was UTF-8 text without embedded nulls).
func markFingerprint(m CriticMark) string {
	if m.Type == CriticSubstitution {
		return string(m.Type) + "\x00" + m.Old + "\x00" + m.New
	}
	return string(m.Type) + "\x00" + m.Text
}

// ParseCriticMarks scans s left-to-right and extracts every CriticMarkup
// mark, returning them sorted by Start. Marks inside fenced or inline code
// are NOT excluded here — that filtering happens at the consumer level via
// `NewCriticMarks` (which subtracts original's marks from markedUp's).
//
// Hand-written scan rather than regex because Go's RE2 doesn't support
// negative lookahead, and a naive `{--([\s\S]*?)--\}` will backtrack across
// other marks' delimiters when the first `--}` doesn't validate the rest of
// the pattern. Each kind here is a deterministic "scan forward to the first
// closing token" operation — no backtracking, no fusion.
//
// Order at each position: substitution (`{~~`) must be tried before
// deletion would be mistaken for it; in practice substitution opens with
// `~~` and deletion with `--`, so the open-brace prefixes already
// disambiguate. The four kinds are mutually exclusive on their 3-char
// opener, so we just dispatch on those bytes.
func ParseCriticMarks(s string) []CriticMark {
	var marks []CriticMark
	for i := 0; i < len(s); {
		if s[i] != '{' || i+3 > len(s) {
			i++
			continue
		}
		opener := s[i+1 : i+3]
		var (
			closer string
			kind   CriticMarkType
		)
		switch opener {
		case "++":
			closer, kind = "++}", CriticInsertion
		case "--":
			closer, kind = "--}", CriticDeletion
		case "~~":
			closer, kind = "~~}", CriticSubstitution
		case ">>":
			closer, kind = "<<}", CriticComment
		default:
			i++
			continue
		}
		bodyStart := i + 3
		end := strings.Index(s[bodyStart:], closer)
		if end < 0 {
			i++
			continue
		}
		body := s[bodyStart : bodyStart+end]
		mark := CriticMark{
			Type:   kind,
			Start:  i,
			Length: 3 + end + len(closer),
		}
		if kind == CriticSubstitution {
			oldText, newText, ok := strings.Cut(body, "~>")
			if !ok {
				// Malformed `{~~...~~}` without `~>` — skip and advance one
				// byte so we don't infinite-loop or eat surrounding text.
				i++
				continue
			}
			mark.Old = oldText
			mark.New = newText
		} else {
			mark.Text = body
		}
		marks = append(marks, mark)
		i = mark.Start + mark.Length
	}
	return marks
}

// CreateOpts holds parameters for creating a new diff review.
type CreateOpts struct {
	BaseCommit string
	HeadCommit string
	HeadRef    string
	Repo       string
	SessionID  string
	IdeaSlug   string
}

// MarkdownCreateOpts holds parameters for creating a new markdown review.
// Path must be absolute. Original is the snapshot of the file content at
// request time — fed back to the human's editor as the unmarked starting
// point.
type MarkdownCreateOpts struct {
	Path      string
	Original  string
	SessionID string
	IdeaSlug  string
}

// ResolveRef resolves any git ref (branch, tag, short SHA, HEAD~3, etc.) to
// a full commit SHA in the given repo.
func ResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	repo, err := openRepo(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolving ref %q: %w", ref, err)
	}
	hash, err := resolveRevision(repo, ref)
	if err != nil {
		return "", fmt.Errorf("resolving ref %q: %w", ref, err)
	}
	return hash.String(), nil
}

// CurrentBranch returns the current branch name in the given repo, or an
// empty string if HEAD is detached.
func CurrentBranch(ctx context.Context, repoPath string) string {
	repo, err := openRepo(repoPath)
	if err != nil {
		return ""
	}
	return currentBranchName(repo)
}

// GenerateReviewID creates a deterministic diff-review ID from resolved
// commit SHAs and head ref. Markdown reviews use a different scheme — see
// GenerateMarkdownReviewID.
func GenerateReviewID(baseCommit, headCommit, headRef string) string {
	baseShort := baseCommit
	if len(baseShort) > 7 {
		baseShort = baseShort[:7]
	}
	headShort := headCommit
	if len(headShort) > 7 {
		headShort = headShort[:7]
	}

	slug := slugify(headRef)
	if slug == "" {
		return baseShort + "-" + headShort
	}
	return baseShort + "-" + headShort + "-" + slug
}

// GenerateMarkdownReviewID creates a deterministic markdown-review ID from
// the absolute path of the file under review. Same path → same ID, so
// successive review rounds on the same file dedup the same way diff reviews
// dedup on commits + ref. The hash component disambiguates files that share
// a basename across directories.
func GenerateMarkdownReviewID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	hash := hex.EncodeToString(sum[:4]) // 8 hex chars
	name := filepath.Base(absPath)
	name = strings.TrimSuffix(name, ".mdx")
	name = strings.TrimSuffix(name, ".md")
	slug := slugify(name)
	if slug == "" {
		return "md-" + hash
	}
	return "md-" + slug + "-" + hash
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ValidID rejects any reviewID that could escape the reviews directory or
// evade the .json suffix glob. IDs from GenerateReviewID always match; the
// check is for defense against caller-supplied IDs (MCP tool args, CLI flags).
func ValidID(id string) error {
	if id == "" {
		return fmt.Errorf("review id is required")
	}
	if len(id) > 80 {
		return fmt.Errorf("review id %q exceeds 80 characters", id)
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("review id %q must match [A-Za-z0-9-]+", id)
	}
	return nil
}
