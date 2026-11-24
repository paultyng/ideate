package model

import (
	"fmt"
	"strings"

	okf "github.com/paultyng/go-okf"
	"gopkg.in/yaml.v3"
)

const frontmatterDelim = "---"

// ParseFrontmatter splits a markdown file into YAML frontmatter and body.
// The file format is:
//
//	---
//	yaml: content
//	---
//	markdown body
func ParseFrontmatter(content string) (yamlStr string, body string, err error) {
	trimmed := strings.TrimLeft(content, "\n")
	if !strings.HasPrefix(trimmed, frontmatterDelim+"\n") {
		// No frontmatter — entire content is body.
		return "", content, nil
	}

	// Skip the opening delimiter.
	rest := trimmed[len(frontmatterDelim)+1:]
	idx := strings.Index(rest, frontmatterDelim+"\n")
	if idx < 0 {
		// Handle case where closing delimiter is at EOF with no trailing newline.
		if strings.HasSuffix(rest, frontmatterDelim) {
			return rest[:len(rest)-len(frontmatterDelim)], "", nil
		}
		return "", "", fmt.Errorf("unclosed frontmatter delimiter")
	}

	yamlStr = rest[:idx]
	body = rest[idx+len(frontmatterDelim)+1:]
	return yamlStr, body, nil
}

// ParseIdeaFile reads an idea.md file content and returns an Idea with its
// Summary populated. Serialization is OKF-native (see okfmap.go); this also
// dual-reads legacy v0.1-shaped frontmatter (name/status/pause_until) so a
// pre-migration idea.md still loads correctly.
func ParseIdeaFile(content string) (*Idea, error) {
	c, err := okf.Parse([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("parsing okf concept: %w", err)
	}
	return ideaFromConcept(c), nil
}

// SerializeIdeaFile creates the content of an idea.md from an Idea struct,
// as an OKF concept document (see okfmap.go).
func SerializeIdeaFile(idea *Idea) (string, error) {
	c := conceptFromIdea(idea)
	return string(c.Bytes()), nil
}

// MarkdownFile represents a non-idea markdown file with optional resource frontmatter.
type MarkdownFile struct {
	Resources []Resource `yaml:"resources,omitempty"`
	Body      string     `yaml:"-"`
}

// ParseMarkdownFile reads any .md file and returns its frontmatter resources + body.
func ParseMarkdownFile(content string) (*MarkdownFile, error) {
	yamlStr, body, err := ParseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	mf := &MarkdownFile{Body: body}
	if yamlStr != "" {
		if err := yaml.Unmarshal([]byte(yamlStr), mf); err != nil {
			return nil, fmt.Errorf("unmarshaling frontmatter: %w", err)
		}
		mf.Body = body
	}
	return mf, nil
}
