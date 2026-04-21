package runtime_test

import (
	"testing"
	"verk/internal/adapters/runtime"
)

func TestNormalizeKey(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"needs-more-context", "needs_more_context"},
		{"needs_more_context", "needs_more_context"},
		{"NEEDS-MORE-CONTEXT", "needs_more_context"},
		{"Needs More Context", "needs_more_context"},
		{"  done  ", "done"},
		{"blocked-by-operator-input", "blocked_by_operator_input"},
		{"done_with_concerns", "done_with_concerns"},
		{"done-with-concerns", "done_with_concerns"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got := runtime.NormalizeKey(tc.input)
			if got != tc.want {
				t.Fatalf("NormalizeKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeWorkerStatusString_NeedsContextVariants(t *testing.T) {
	// All of these raw strings must resolve to WorkerStatusNeedsContext.
	variants := []string{
		"needs_context",
		"needs-context",
		"needscontext",
		"context_needed",
		"context-needed",
		"needs_more_context",
		"needs-more-context",
		"needs more context",
		"NEEDS-MORE-CONTEXT",
		"Needs More Context",
		"  needs_context  ",
		"NEEDS_CONTEXT",
		"needs-more_context",
		"needsMoreContext",
	}
	for _, raw := range variants {
		t.Run(raw, func(t *testing.T) {
			status, ok := runtime.NormalizeWorkerStatusString(raw)
			if !ok {
				t.Fatalf("NormalizeWorkerStatusString(%q) returned ok=false, want true", raw)
			}
			if status != runtime.WorkerStatusNeedsContext {
				t.Fatalf("NormalizeWorkerStatusString(%q) = %q, want %q", raw, status, runtime.WorkerStatusNeedsContext)
			}
		})
	}
}

func TestNormalizeWorkerStatusString_AllStatuses(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		want   runtime.WorkerStatus
		wantOK bool
	}{
		// Done variants
		{"done", runtime.WorkerStatusDone, true},
		{"completed", runtime.WorkerStatusDone, true},
		{"complete", runtime.WorkerStatusDone, true},
		{"success", runtime.WorkerStatusDone, true},
		{"passed", runtime.WorkerStatusDone, true},
		{"ok", runtime.WorkerStatusDone, true},
		{"DONE", runtime.WorkerStatusDone, true},

		// DoneWithConcerns variants
		{"done_with_concerns", runtime.WorkerStatusDoneWithConcerns, true},
		{"done-with-concerns", runtime.WorkerStatusDoneWithConcerns, true},
		{"donewithconcerns", runtime.WorkerStatusDoneWithConcerns, true},
		{"concerns", runtime.WorkerStatusDoneWithConcerns, true},

		// NeedsContext variants (hyphenated and underscored)
		{"needs_context", runtime.WorkerStatusNeedsContext, true},
		{"needs-context", runtime.WorkerStatusNeedsContext, true},
		{"needs_more_context", runtime.WorkerStatusNeedsContext, true},
		{"needs-more-context", runtime.WorkerStatusNeedsContext, true},
		{"needscontext", runtime.WorkerStatusNeedsContext, true},
		{"context_needed", runtime.WorkerStatusNeedsContext, true},
		{"needsMoreContext", runtime.WorkerStatusNeedsContext, true},
		{"needsmorecontext", runtime.WorkerStatusNeedsContext, true},

		// Blocked variants
		{"blocked", runtime.WorkerStatusBlocked, true},
		{"blocked_by_operator_input", runtime.WorkerStatusBlocked, true},
		{"blocked-by-operator-input", runtime.WorkerStatusBlocked, true},
		{"blockedbyoperatorinput", runtime.WorkerStatusBlocked, true},

		// Unknown → not recognized
		{"running", "", false},
		{"error", "", false},
		{"", "", false},
		{"unknown_status", "", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			status, ok := runtime.NormalizeWorkerStatusString(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("NormalizeWorkerStatusString(%q) ok=%v, want %v", tc.raw, ok, tc.wantOK)
			}
			if status != tc.want {
				t.Fatalf("NormalizeWorkerStatusString(%q) = %q, want %q", tc.raw, status, tc.want)
			}
		})
	}
}

func TestNormalizeWorkerStatusString_NeedsContextIsBlocking(t *testing.T) {
	// Verify that WorkerStatusNeedsContext is not one of the success statuses.
	// This guards against the engine treating needs_context as success.
	status, ok := runtime.NormalizeWorkerStatusString("needs-more-context")
	if !ok {
		t.Fatalf("needs-more-context not recognized")
	}
	if status == runtime.WorkerStatusDone || status == runtime.WorkerStatusDoneWithConcerns {
		t.Fatalf("needs-more-context must not normalize to a success status, got %q", status)
	}
	if status != runtime.WorkerStatusNeedsContext {
		t.Fatalf("expected WorkerStatusNeedsContext, got %q", status)
	}
}
