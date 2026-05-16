package runtime

import (
	"embed"
	"path/filepath"
	"strings"
)

//go:embed standards/*.md
var standardsFS embed.FS

// MaxWorkerStandardsBytes is a soft limit on the rendered standards block
// injected into worker prompts. If the rendered block exceeds this size it is
// truncated with a clear marker so the worker still sees a usable subset.
const MaxWorkerStandardsBytes = 32_768 // 32 KiB

// DetectLanguagesFromPaths returns the distinct programming languages inferred
// from a list of file paths by examining each path's file extension.
func DetectLanguagesFromPaths(paths []string) []string {
	seen := make(map[string]bool)
	var langs []string
	for _, p := range paths {
		lang := extToLanguage(filepath.Ext(p))
		if lang != "" && !seen[lang] {
			seen[lang] = true
			langs = append(langs, lang)
		}
	}
	return langs
}

// DetectLanguages returns the distinct programming languages present in a unified
// diff by scanning "+++ b/<path>" header lines for known file extensions.
func DetectLanguages(diff string) []string {
	seen := make(map[string]bool)
	var langs []string
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+++ b/") {
			continue
		}
		path := strings.TrimPrefix(line, "+++ b/")
		lang := extToLanguage(filepath.Ext(path))
		if lang != "" && !seen[lang] {
			seen[lang] = true
			langs = append(langs, lang)
		}
	}
	return langs
}

func extToLanguage(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs":
		return "javascript"
	case ".rs":
		return "rust"
	case ".sh", ".bash", ".zsh":
		return "shell"
	default:
		return ""
	}
}

// truncateStandards truncates a standards block to MaxWorkerStandardsBytes,
// appending a clear marker line when truncation occurs.
func truncateStandards(s string) string {
	if len(s) <= MaxWorkerStandardsBytes {
		return s
	}
	return s[:MaxWorkerStandardsBytes] + "\n[standards truncated: 32 KiB limit reached]\n"
}

// BuildReviewStandards assembles review standards to inject into a reviewer prompt.
// It always includes the universal standards and the cross-platform checklist, then
// adds any language-specific standards for each detected language.
func BuildReviewStandards(languages []string) string {
	var b strings.Builder

	appendFile := func(name string) {
		content, err := standardsFS.ReadFile(name)
		if err != nil {
			return
		}
		b.Write(content)
		b.WriteString("\n\n")
	}

	appendFile("standards/universal.md")

	for _, lang := range languages {
		appendFile("standards/" + lang + ".md")
	}

	appendFile("standards/cross_platform.md")

	return strings.TrimSpace(b.String())
}
