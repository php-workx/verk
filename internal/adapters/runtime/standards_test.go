package runtime

import (
	"strings"
	"testing"
)

func TestDetectLanguages_GoFiles(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1 +1 @@
-old
+new
diff --git a/bar.go b/bar.go
--- a/bar.go
+++ b/bar.go`

	langs := DetectLanguages(diff)
	if len(langs) != 1 || langs[0] != "go" {
		t.Errorf("DetectLanguages got %v, want [go]", langs)
	}
}

func TestDetectLanguages_MixedFiles(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
diff --git a/script.py b/script.py
--- a/script.py
+++ b/script.py
diff --git a/lib.ts b/lib.ts
--- a/lib.ts
+++ b/lib.ts`

	langs := DetectLanguages(diff)
	seen := make(map[string]bool)
	for _, l := range langs {
		seen[l] = true
	}
	for _, want := range []string{"go", "python", "typescript"} {
		if !seen[want] {
			t.Errorf("DetectLanguages missing %q, got %v", want, langs)
		}
	}
}

func TestDetectLanguages_EmptyDiff(t *testing.T) {
	langs := DetectLanguages("")
	if len(langs) != 0 {
		t.Errorf("DetectLanguages empty diff got %v, want []", langs)
	}
}

func TestDetectLanguages_UnknownExtension(t *testing.T) {
	diff := "+++ b/config.yaml"
	langs := DetectLanguages(diff)
	if len(langs) != 0 {
		t.Errorf("DetectLanguages unknown ext got %v, want []", langs)
	}
}

func TestBuildReviewStandards_AlwaysIncludesUniversal(t *testing.T) {
	standards := BuildReviewStandards(nil)
	if !strings.Contains(standards, "Universal Review Standards") {
		t.Errorf("BuildReviewStandards missing universal standards section")
	}
}

func TestBuildReviewStandards_AlwaysIncludesCrossPlatform(t *testing.T) {
	standards := BuildReviewStandards(nil)
	if !strings.Contains(standards, "Cross-Platform") {
		t.Errorf("BuildReviewStandards missing cross-platform section")
	}
}

func TestBuildReviewStandards_InjectsGoChecklist(t *testing.T) {
	standards := BuildReviewStandards([]string{"go"})
	if !strings.Contains(standards, "Go-Specific Review Checklist") {
		t.Errorf("BuildReviewStandards missing Go checklist for language=go")
	}
}

func TestBuildReviewStandards_SkipsUnknownLanguage(t *testing.T) {
	// Should not panic or error on unknown languages; just skips the file.
	standards := BuildReviewStandards([]string{"cobol"})
	if !strings.Contains(standards, "Universal Review Standards") {
		t.Errorf("BuildReviewStandards should still include universal for unknown language")
	}
}

func TestBuildReviewStandards_NonEmpty(t *testing.T) {
	standards := BuildReviewStandards([]string{"go"})
	if len(strings.TrimSpace(standards)) == 0 {
		t.Errorf("BuildReviewStandards returned empty string")
	}
}

func TestDetectLanguagesFromPaths_DetectsGo(t *testing.T) {
	paths := []string{"internal/engine/ticket_run.go", "internal/engine/ticket_run_test.go"}
	langs := DetectLanguagesFromPaths(paths)
	if len(langs) != 1 || langs[0] != "go" {
		t.Errorf("DetectLanguagesFromPaths got %v, want [go]", langs)
	}
}

func TestDetectLanguagesFromPaths_DetectsTypeScript(t *testing.T) {
	paths := []string{"src/app.ts", "src/component.tsx"}
	langs := DetectLanguagesFromPaths(paths)
	if len(langs) != 1 || langs[0] != "typescript" {
		t.Errorf("DetectLanguagesFromPaths got %v, want [typescript]", langs)
	}
}

func TestDetectLanguagesFromPaths_DetectsMixed(t *testing.T) {
	paths := []string{"main.go", "script.py", "app.ts"}
	langs := DetectLanguagesFromPaths(paths)
	seen := make(map[string]bool)
	for _, l := range langs {
		seen[l] = true
	}
	for _, want := range []string{"go", "python", "typescript"} {
		if !seen[want] {
			t.Errorf("DetectLanguagesFromPaths missing %q, got %v", want, langs)
		}
	}
}

func TestDetectLanguagesFromPaths_EmptyPaths(t *testing.T) {
	langs := DetectLanguagesFromPaths(nil)
	if len(langs) != 0 {
		t.Errorf("DetectLanguagesFromPaths nil paths got %v, want []", langs)
	}
}

func TestDetectLanguagesFromPaths_UnknownExtension(t *testing.T) {
	paths := []string{"config.yaml", "data.json"}
	langs := DetectLanguagesFromPaths(paths)
	if len(langs) != 0 {
		t.Errorf("DetectLanguagesFromPaths unknown ext got %v, want []", langs)
	}
}

func TestBuildReviewStandards_TruncatesAtBudget(t *testing.T) {
	// Build a standards string that exceeds the budget by repeating the go standards
	// content many times, which we then feed through the truncation helper directly.
	base := BuildReviewStandards([]string{"go"})
	// Construct oversized input by hand (base repeated enough to exceed 32 KiB).
	var b strings.Builder
	for b.Len() < MaxWorkerStandardsBytes+len(base) {
		b.WriteString(base)
	}
	oversized := b.String()

	result := truncateStandards(oversized)
	if len(result) > MaxWorkerStandardsBytes+len("\n[standards truncated: 32 KiB limit reached]\n") {
		t.Errorf("truncateStandards result too long: got %d bytes", len(result))
	}
	if !strings.Contains(result, "[standards truncated: 32 KiB limit reached]") {
		t.Errorf("expected truncation marker in result")
	}
}

func TestTruncateStandards_NoTruncationWhenUnderBudget(t *testing.T) {
	standards := BuildReviewStandards([]string{"go"})
	// The actual standards should be well under 32 KiB; no truncation expected.
	result := truncateStandards(standards)
	if result != standards {
		t.Errorf("expected no truncation for standards under budget")
	}
	if strings.Contains(result, "[standards truncated") {
		t.Errorf("unexpected truncation marker in result")
	}
}
