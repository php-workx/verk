package constraints

import (
	"testing"
)

func TestDeriveSignature_Stable(t *testing.T) {
	t.Parallel()
	sig1 := DeriveSignature("return nil in scope check functions", "internal/engine/closeout.go", "P2")
	sig2 := DeriveSignature("return nil in scope check functions", "internal/engine/closeout.go", "P2")
	if sig1 != sig2 {
		t.Fatalf("expected stable signature, got %q vs %q", sig1, sig2)
	}
}

func TestDeriveSignature_DifferentSeverityBucket(t *testing.T) {
	t.Parallel()
	sigP2 := DeriveSignature("missing error check in handler", "internal/engine/ticket_run.go", "P2")
	sigP1 := DeriveSignature("missing error check in handler", "internal/engine/ticket_run.go", "P1")
	if sigP2 == sigP1 {
		t.Fatalf("expected different signatures for P2 vs P1, got same: %q", sigP2)
	}

	// P0 and P1 should share a bucket
	sigP0 := DeriveSignature("missing error check in handler", "internal/engine/ticket_run.go", "P0")
	if sigP0 != sigP1 {
		t.Fatalf("expected same signature for P0 and P1 (same bucket), got %q vs %q", sigP0, sigP1)
	}

	// P3 and P4 should share a bucket
	sigP3 := DeriveSignature("missing error check in handler", "internal/engine/ticket_run.go", "P3")
	sigP4 := DeriveSignature("missing error check in handler", "internal/engine/ticket_run.go", "P4")
	if sigP3 != sigP4 {
		t.Fatalf("expected same signature for P3 and P4 (same bucket), got %q vs %q", sigP3, sigP4)
	}
}

func TestDeriveSignature_DifferentFiles(t *testing.T) {
	t.Parallel()
	sig1 := DeriveSignature("nil pointer dereference risk found", "internal/engine/closeout.go", "P2")
	sig2 := DeriveSignature("nil pointer dereference risk found", "internal/cli/run.go", "P2")
	if sig1 == sig2 {
		t.Fatalf("expected different signatures for different files, got same: %q", sig1)
	}
}

func TestDeriveSignature_SameDirectorySameExt(t *testing.T) {
	t.Parallel()
	// Files in same directory with same extension should yield same glob → same sig
	sig1 := DeriveSignature("error not wrapped properly here", "internal/engine/closeout.go", "P2")
	sig2 := DeriveSignature("error not wrapped properly here", "internal/engine/ticket_run.go", "P2")
	if sig1 != sig2 {
		t.Fatalf("expected same signature for files in same dir/ext, got %q vs %q", sig1, sig2)
	}
}

func TestFileToGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"internal/engine/epic_run.go", "internal/engine/*.go"},
		{"internal/engine/closeout.go", "internal/engine/*.go"},
		{"cmd/main.go", "cmd/*.go"},
		{"main.go", "./*.go"},
		{"noext", "noext"},
		{"path/to/file.ts", "path/to/*.ts"},
	}
	for _, tc := range tests {
		got := fileToGlob(tc.input)
		if got != tc.want {
			t.Errorf("fileToGlob(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSeverityBucket(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sev  string
		want string
	}{
		{"P0", "P0-P1"},
		{"P1", "P0-P1"},
		{"P2", "P2"},
		{"P3", "P3-P4"},
		{"P4", "P3-P4"},
		{"unknown", "other"},
		{"", "other"},
	}
	for _, tc := range tests {
		got := severityBucket(tc.sev)
		if got != tc.want {
			t.Errorf("severityBucket(%q) = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

func TestNormalizeTitlePrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"Return nil in scope check functions", "return nil in scope check functions"},
		{"Missing error check: handler returns nil", "missing error check handler returns nil"},
		{"One Two Three Four Five Six Seven Eight", "one two three four five six"},
		{"  spaces   everywhere  ", "spaces everywhere"},
		{"UPPERCASE TITLE WITH PUNCT!", "uppercase title with punct"},
	}
	for _, tc := range tests {
		got := normalizeTitlePrefix(tc.input)
		if got != tc.want {
			t.Errorf("normalizeTitlePrefix(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
