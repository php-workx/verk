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
