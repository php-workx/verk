package cli

import (
	"os"
	"testing"

	"verk/internal/state"
)

func TestIsValidReopenPhase(t *testing.T) {
	tests := []struct {
		phase string
		want  bool
	}{
		{"implement", true},
		{"repair", true},
		{"", false},
		{"bananas", false},
		{"blocked", false},
		{"closed", false},
		{"review", false},
		{"IMPLEMENT", false},  // case-sensitive
		{"implement ", false}, // whitespace not trimmed
	}
	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := isValidReopenPhase(tt.phase)
			if got != tt.want {
				t.Errorf("isValidReopenPhase(%q) = %v, want %v", tt.phase, got, tt.want)
			}
		})
	}
}

func TestReopenCmd_RejectsInvalidPhase(t *testing.T) {
	// Verify that --to with an invalid phase produces an error before
	// the engine is called. We exercise the CLI directly via ExecuteArgs.
	tests := []struct {
		name   string
		toFlag string
	}{
		{"invalid phase", "bananas"},
		{"empty phase", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := osPipe(t)
			exitCode := ExecuteArgs(
				[]string{"reopen", "run-abc", "ticket-abc", "--to", tt.toFlag},
				stdout, stderr,
			)
			if exitCode == 0 {
				t.Fatalf("expected non-zero exit code for --to=%q, got 0", tt.toFlag)
			}
		})
	}
}

func TestReopenCmd_ValidPhasesPassValidation(t *testing.T) {
	for _, phase := range []string{"implement", "repair"} {
		t.Run(phase, func(t *testing.T) {
			// isValidReopenPhase should return true for valid phases.
			// We can't test full CLI success (needs git repo + artifacts),
			// but we verify the validation function accepts them.
			if !isValidReopenPhase(phase) {
				t.Fatalf("isValidReopenPhase(%q) = false, want true", phase)
			}
		})
	}
}

func TestReopenPhaseAllowList(t *testing.T) {
	tests := []struct {
		name string
		to   state.TicketPhase
		want bool
	}{
		{"implement allowed", state.TicketPhaseImplement, true},
		{"repair allowed", state.TicketPhaseRepair, true},
		{"blocked rejected", state.TicketPhaseBlocked, false},
		{"closed rejected", state.TicketPhaseClosed, false},
		{"review rejected", state.TicketPhaseReview, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidReopenPhase(string(tt.to))
			if got != tt.want {
				t.Fatalf("isValidReopenPhase(%q)=%v, want %v", tt.to, got, tt.want)
			}
		})
	}
}

// osPipe creates a pair of writable temp files for capturing CLI stdout and stderr.
// Using temp files avoids read-end/write-end confusion that arises with os.Pipe.
func osPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	t.Cleanup(func() { _ = stdout.Close() })
	t.Cleanup(func() { _ = stderr.Close() })
	return stdout, stderr
}
