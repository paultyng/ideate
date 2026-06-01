package review

import (
	"path/filepath"
	"strings"
)

var extToLanguage = map[string]string{
	".go":         "go",
	".ts":         "typescript",
	".tsx":        "typescript",
	".js":         "javascript",
	".jsx":        "javascript",
	".py":         "python",
	".rs":         "rust",
	".java":       "java",
	".rb":         "ruby",
	".css":        "css",
	".html":       "html",
	".json":       "json",
	".yaml":       "yaml",
	".yml":        "yaml",
	".md":         "markdown",
	".sql":        "sql",
	".sh":         "bash",
	".bash":       "bash",
	".proto":      "protobuf",
	".toml":       "toml",
	".xml":        "xml",
	".c":          "c",
	".h":          "c",
	".cpp":        "cpp",
	".cc":         "cpp",
	".hpp":        "cpp",
	".cs":         "csharp",
	".swift":      "swift",
	".kt":         "kotlin",
	".dockerfile": "dockerfile",
}

var nameToLanguage = map[string]string{
	"Dockerfile": "dockerfile",
}

func detectLanguage(filename string) string {
	base := filepath.Base(filename)
	if lang, ok := nameToLanguage[base]; ok {
		return lang
	}
	ext := strings.ToLower(filepath.Ext(base))
	if lang, ok := extToLanguage[ext]; ok {
		return lang
	}
	return ""
}
