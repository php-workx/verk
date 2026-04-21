package engine

import (
	"regexp"
	"strings"
)

func buildStaleWordingGrepCommand(terms, docPaths []string, invert bool) string {
	if len(docPaths) == 0 {
		docPaths = defaultEpicClosureDocs
	}
	quotedPattern := shellQuote(staleWordingPattern(terms))
	quotedPaths := make([]string, 0, len(docPaths))
	for _, path := range docPaths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		quotedPaths = append(quotedPaths, shellQuote(trimmed))
	}
	if len(quotedPaths) == 0 {
		if invert {
			return "true"
		}
		return "false"
	}

	command := "{ stale_found=1; for p in " + strings.Join(quotedPaths, " ") + "; do [ -e \"$p\" ] || continue; grep -nE -r " + quotedPattern + " \"$p\" && stale_found=0; done; [ \"$stale_found\" -eq 0 ]; }"
	if invert {
		return "! " + command
	}
	return command
}

func staleWordingPattern(terms []string) string {
	patterns := make([]string, 0, len(terms))
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		patterns = append(patterns, regexp.QuoteMeta(trimmed))
	}
	return strings.Join(patterns, "|")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
