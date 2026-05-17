package epos

import (
	"path/filepath"
	"strings"
)

// DetectProfile returns the role profile that best matches the ticket.
// Priority: security > contract > frontend > backend (fallback).
// Pure function, no I/O, no LLM.
func DetectProfile(t Ticket) string {
	if matchesSecurity(t) {
		return ProfileSecurity
	}
	if matchesContract(t) {
		return ProfileContract
	}
	if matchesFrontend(t) {
		return ProfileFrontend
	}
	return ProfileBackend
}

// ticketTags extracts tags stored in UnknownFrontmatter["tags"].
func ticketTags(t Ticket) []string {
	if t.UnknownFrontmatter == nil {
		return nil
	}
	raw, ok := t.UnknownFrontmatter["tags"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func hasTag(t Ticket, targets ...string) bool {
	tags := ticketTags(t)
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		for _, target := range targets {
			if strings.EqualFold(normalized, target) {
				return true
			}
		}
	}
	return false
}

func ownedPathExts(t Ticket) []string {
	exts := make([]string, 0, len(t.OwnedPaths))
	for _, p := range t.OwnedPaths {
		if ext := filepath.Ext(p); ext != "" {
			exts = append(exts, strings.ToLower(ext))
		}
	}
	return exts
}

func hasOwnedExt(t Ticket, targets ...string) bool {
	for _, ext := range ownedPathExts(t) {
		for _, target := range targets {
			if strings.EqualFold(ext, target) {
				return true
			}
		}
	}
	return false
}

func criteriaText(t Ticket) string {
	return strings.Join(t.AcceptanceCriteria, "\n")
}

func containsAnyCaseInsensitive(haystack string, needles []string) bool {
	lc := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(lc, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func matchesSecurity(t Ticket) bool {
	if hasTag(t, "security", "auth", "hardening", "CVE", "pentest") {
		return true
	}
	securityKeywords := []string{
		"token", "credential", "redact", "signing", "sandbox",
		"injection", "privilege", "secret", "auth",
	}
	if containsAnyCaseInsensitive(t.Body, securityKeywords) {
		return true
	}
	if containsAnyCaseInsensitive(criteriaText(t), securityKeywords) {
		return true
	}
	if hasOwnedExt(t, ".pem", ".key", ".crt") {
		return true
	}
	return false
}

func matchesContract(t Ticket) bool {
	if hasTag(t, "cli", "api", "sdk", "interface", "contract") {
		return true
	}
	contractCriteriaKeywords := []string{
		"exit code", "exit_code", "--flag", "--help", "subcommand",
		"public API", "backward compat", "export", "endpoint",
		"versioning", "pagination", "contract",
	}
	if containsAnyCaseInsensitive(criteriaText(t), contractCriteriaKeywords) {
		return true
	}
	contractBodyKeywords := []string{
		"CLI", "argparse", "cobra", "public surface", "wire format",
		"RPC", "gRPC", "REST",
	}
	return containsAnyCaseInsensitive(t.Body, contractBodyKeywords)
}

func matchesFrontend(t Ticket) bool {
	if hasTag(t, "frontend", "ui", "browser", "a11y") {
		return true
	}
	if hasOwnedExt(t, ".tsx", ".jsx", ".vue", ".svelte", ".css", ".scss", ".html") {
		return true
	}
	frontendKeywords := []string{
		"component", "render", "DOM", "accessibility", "responsive",
		"CSS", "layout", "browser", "viewport",
	}
	if containsAnyCaseInsensitive(t.Body, frontendKeywords) {
		return true
	}
	if containsAnyCaseInsensitive(criteriaText(t), frontendKeywords) {
		return true
	}
	return false
}
