package constraints

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Signature is a deterministic, opaque key derived from finding title prefix,
// file glob, and severity bucket.
type Signature string

// DeriveSignature computes a signature for a review finding.
func DeriveSignature(titlePrefix6Words, filePath, severity string) Signature {
	glob := fileToGlob(filePath)
	bucket := severityBucket(severity)
	titleNorm := normalizeTitlePrefix(titlePrefix6Words)
	raw := fmt.Sprintf("%s|%s|%s", titleNorm, glob, bucket)
	sum := sha256.Sum256([]byte(raw))
	return Signature(fmt.Sprintf("%x", sum[:8]))
}

// fileToGlob maps a concrete file path to a directory glob.
// "internal/engine/epic_run.go" → "internal/engine/*.go"
func fileToGlob(path string) string {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	if ext == "" {
		return path
	}
	return dir + "/*" + ext
}

// severityBucket maps severity to bucket.
// P0,P1 → "P0-P1"; P2 → "P2"; P3,P4 → "P3-P4"; else → "other"
func severityBucket(severity string) string {
	switch severity {
	case "P0", "P1":
		return "P0-P1"
	case "P2":
		return "P2"
	case "P3", "P4":
		return "P3-P4"
	default:
		return "other"
	}
}

// normalizeTitlePrefix lowercases, strips punctuation, takes first 6 words.
func normalizeTitlePrefix(title string) string {
	// lowercase
	lower := strings.ToLower(title)
	// replace non-alphanumeric with space
	var b strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	// split on whitespace
	words := strings.Fields(b.String())
	// take at most 6 words
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.Join(words, " ")
}
