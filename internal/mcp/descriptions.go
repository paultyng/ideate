package mcp

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed descriptions/*.md
var descriptionsFS embed.FS

// desc loads the embedded Markdown description for a tool by name and
// trims trailing whitespace/newlines. A missing file is a build-time
// programmer error (the //go:embed directive guarantees presence at
// compile time only if the file exists in the tree), so we panic
// rather than return an error — every tool definition must have a
// matching descriptions/<name>.md.
func desc(name string) string {
	data, err := descriptionsFS.ReadFile("descriptions/" + name + ".md")
	if err != nil {
		panic(fmt.Sprintf("mcp: missing description file descriptions/%s.md: %v", name, err))
	}
	return strings.TrimRight(string(data), " \t\r\n")
}
