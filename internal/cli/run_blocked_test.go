package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/state"
)

// fakeBlocked builds a BlockedRunError with two blocked tickets. The test cases
// in the ticket mandate that the guidance names both tickets and emits a
// reopen command for each — the two-ticket shape exercises the "multiple
// blocked tickets" case that a one-ticket fixture would miss.
func fakeBlocked() *engine.BlockedRunError {
	return &engine.BlockedRunError{
		RunID:  "run-ver-vmgr-123-ver-epic",
		Status: state.EpicRunStatusBlocked,
		BlockedTickets: []engine.BlockedTicket{
			{
				ID:         "ver-uqs5",
				Title:      "Example ticket",
				Status:     tkmd.StatusBlocked,
				Phase:      state.TicketPhaseBlocked,
				RetryPhase: state.TicketPhaseImplement,
				Reason:     "needs context: reviewer asked for missing fixture details",
			},
			{
				ID:         "ver-abcd",
				Title:      "Second ticket",
				Status:     tkmd.StatusBlocked,
				Phase:      state.TicketPhaseBlocked,
				RetryPhase: state.TicketPhaseImplement,
				Reason:     "repair limit exceeded",
			},
		},
	}
}

func TestPrintBlockedRunGuidance_IncludesTicketIDsReasonsAndRetryCommands(t *testing.T) {
	var buf bytes.Buffer
	printBlockedRunGuidance(&buf, fakeBlocked())
	out := buf.String()

	wants := []string{
		"Blocked tickets:",
		"ver-uqs5",
		"needs context: reviewer asked for missing fixture details",
		"ver-abcd",
		"repair limit exceeded",
		"Retry:",
		"verk reopen run-ver-vmgr-123-ver-epic ver-uqs5 --to implement",
		"verk reopen run-ver-vmgr-123-ver-epic ver-abcd --to implement",
		"verk run",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("expected guidance to contain %q, got:\n%s", want, out)
		}
	}
}

func TestPrintBlockedRunGuidance_FallbackReasonWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	printBlockedRunGuidance(&buf, &engine.BlockedRunError{
		RunID:  "run-x",
		Status: state.EpicRunStatusBlocked,
		BlockedTickets: []engine.BlockedTicket{
			{ID: "ver-none", Reason: ""},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "ver-none") {
		t.Fatalf("expected ticket id in output, got:\n%s", out)
	}
	if !strings.Contains(out, "no reason recorded") {
		t.Fatalf("expected fallback reason in output, got:\n%s", out)
	}
}

func TestPrintBlockedRunGuidance_SkipsNonRetryableDependencyWaiters(t *testing.T) {
	var buf bytes.Buffer
	printBlockedRunGuidance(&buf, &engine.BlockedRunError{
		RunID:  "run-x",
		Status: state.EpicRunStatusBlocked,
		BlockedTickets: []engine.BlockedTicket{
			{
				ID:     "mm-4for",
				Status: tkmd.StatusOpen,
				Phase:  state.TicketPhaseIntake,
				Reason: "waiting on 7 deps",
			},
			{
				ID:         "mm-e7e1",
				Status:     tkmd.StatusBlocked,
				Phase:      state.TicketPhaseBlocked,
				RetryPhase: state.TicketPhaseImplement,
				Reason:     "verification artifact passed flag contradicts derived verification outcome",
			},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "mm-4for: waiting on 7 deps") {
		t.Fatalf("expected non-retryable blocker to be listed, got:\n%s", out)
	}
	if strings.Contains(out, "verk reopen run-x mm-4for") {
		t.Fatalf("dependency-waiting tickets must not be offered for reopen, got:\n%s", out)
	}
	if !strings.Contains(out, "verk reopen run-x mm-e7e1 --to implement") {
		t.Fatalf("expected retry command for blocked ticket, got:\n%s", out)
	}
}

func TestPrintBlockedRunGuidance_NoTickets(t *testing.T) {
	var buf bytes.Buffer
	printBlockedRunGuidance(&buf, &engine.BlockedRunError{
		RunID:  "run-x",
		Status: state.EpicRunStatusBlocked,
		Cause:  errors.New("wave verification failed"),
	})
	out := buf.String()
	if !strings.Contains(out, "Epic run blocked") {
		t.Fatalf("expected summary line when no tickets, got:\n%s", out)
	}
	if !strings.Contains(out, "wave verification failed") {
		t.Fatalf("expected cause to be rendered when no tickets, got:\n%s", out)
	}
}

func TestPromptBlockedRetry_DefaultNoSelection(t *testing.T) {
	// Empty answers for every prompt: both tickets should be skipped.
	in := strings.NewReader("\n\n")
	var out bytes.Buffer
	selected, cancelled := promptBlockedRetry(context.Background(), in, &out, fakeBlocked())
	if cancelled {
		t.Fatal("did not expect prompt cancellation")
	}
	if len(selected) != 0 {
		t.Fatalf("expected no tickets selected by default, got %v", selected)
	}
	// The prompt must mention both ticket IDs so operators can see what they
	// are being asked about.
	text := out.String()
	if !strings.Contains(text, "ver-uqs5") || !strings.Contains(text, "ver-abcd") {
		t.Fatalf("expected prompts for both tickets, got:\n%s", text)
	}
}

func TestPromptBlockedRetry_SelectsYesAnswers(t *testing.T) {
	in := strings.NewReader("y\nn\n")
	var out bytes.Buffer
	selected, cancelled := promptBlockedRetry(context.Background(), in, &out, fakeBlocked())
	if cancelled {
		t.Fatal("did not expect prompt cancellation")
	}
	if len(selected) != 1 || selected[0].ID != "ver-uqs5" {
		t.Fatalf("expected only ver-uqs5 selected, got %v", selected)
	}
}

func TestPromptBlockedRetry_SkipsNonRetryableTickets(t *testing.T) {
	blocked := &engine.BlockedRunError{
		RunID:  "run-x",
		Status: state.EpicRunStatusBlocked,
		BlockedTickets: []engine.BlockedTicket{
			{ID: "mm-4for", Status: tkmd.StatusOpen, Phase: state.TicketPhaseIntake, Reason: "waiting on 7 deps"},
			{ID: "mm-e7e1", Status: tkmd.StatusBlocked, Phase: state.TicketPhaseBlocked, RetryPhase: state.TicketPhaseImplement, Reason: "blocked"},
		},
	}
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	selected, cancelled := promptBlockedRetry(context.Background(), in, &out, blocked)
	if cancelled {
		t.Fatal("did not expect prompt cancellation")
	}
	if len(selected) != 1 || selected[0].ID != "mm-e7e1" {
		t.Fatalf("expected only retryable ticket selected, got %v", selected)
	}
	if strings.Contains(out.String(), "mm-4for") {
		t.Fatalf("non-retryable ticket should not be prompted, got:\n%s", out.String())
	}
}

func TestPromptBlockedRetry_EOFStopsLoop(t *testing.T) {
	// Scanner returns false on EOF before any line: no selections, no panic.
	in := strings.NewReader("")
	var out bytes.Buffer
	selected, cancelled := promptBlockedRetry(context.Background(), in, &out, fakeBlocked())
	if cancelled {
		t.Fatal("did not expect prompt cancellation")
	}
	if len(selected) != 0 {
		t.Fatalf("expected no selections on EOF, got %v", selected)
	}
}

func TestPromptBlockedRetry_ContextCancellationStopsBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	var out bytes.Buffer

	done := make(chan struct {
		selected  []engine.BlockedTicket
		cancelled bool
	}, 1)
	go func() {
		selected, cancelled := promptBlockedRetry(ctx, reader, &out, fakeBlocked())
		done <- struct {
			selected  []engine.BlockedTicket
			cancelled bool
		}{selected: selected, cancelled: cancelled}
	}()

	cancel()

	select {
	case result := <-done:
		if !result.cancelled {
			t.Fatal("expected prompt cancellation")
		}
		if len(result.selected) != 0 {
			t.Fatalf("expected no selected tickets, got %v", result.selected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not return after context cancellation")
	}
}

func TestPromptBlockedRetryKeys_CancelKeysAbortWithoutEnter(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "ctrl+c", key: "\x03"},
		{name: "ctrl+x", key: "\x18"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			selected, cancelled := promptBlockedRetryKeys(context.Background(), strings.NewReader(tt.key), &out, fakeBlocked())
			if !cancelled {
				t.Fatal("expected prompt cancellation")
			}
			if len(selected) != 0 {
				t.Fatalf("expected no selected tickets, got %v", selected)
			}
		})
	}
}

func TestAsBlockedRunError_MatchesOnlyBlockedRunError(t *testing.T) {
	if _, ok := asBlockedRunError(nil); ok {
		t.Fatal("nil error should not match BlockedRunError")
	}
	if _, ok := asBlockedRunError(errors.New("other")); ok {
		t.Fatal("unrelated error should not match BlockedRunError")
	}
	if _, ok := asBlockedRunError(engine.ErrEpicBlocked); ok {
		t.Fatal("sentinel should not be treated as a BlockedRunError value")
	}
	blocked, ok := asBlockedRunError(fakeBlocked())
	if !ok || blocked == nil {
		t.Fatal("expected match for BlockedRunError value")
	}
	// Errors.Is still works for the sentinel so older callers are unaffected.
	if !errors.Is(blocked, engine.ErrEpicBlocked) {
		t.Fatal("BlockedRunError must unwrap to ErrEpicBlocked")
	}
}

// TestHandleBlockedEpicRun_NonTTYExitsWithoutPrompt verifies that when stdin/
// stdout are not TTYs, handleBlockedEpicRun returns retried=false and does not
// read from the interactor. This is the safety net that prevents CI runs from
// hanging on a prompt.
func TestHandleBlockedEpicRun_NonTTYExitsWithoutPrompt(t *testing.T) {
	original := blockedRunInteractorFor
	t.Cleanup(func() { blockedRunInteractorFor = original })

	// Inject an interactor with nil file handles — isTTY returns false.
	blockedRunInteractorFor = func() blockedRunInteractor {
		return blockedRunInteractor{in: nil, out: nil}
	}

	var out, errw bytes.Buffer
	retried, err := handleBlockedEpicRun(context.Background(), &out, &errw, t.TempDir(), contextCfgForResume{}, fakeBlocked())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried {
		t.Fatal("expected retried=false in non-TTY mode")
	}
	// Guidance must be printed to errw so operators see why the run stopped.
	if !strings.Contains(errw.String(), "Blocked tickets:") {
		t.Fatalf("expected guidance in errw, got:\n%s", errw.String())
	}
}

// TestBlockedRunErrorMessage_SummarizesTickets checks the Error() text used by
// callers that print the error verbatim. The compact summary must mention the
// sentinel, the status, and the blocked ticket IDs.
func TestBlockedRunErrorMessage_SummarizesTickets(t *testing.T) {
	msg := fakeBlocked().Error()
	for _, want := range []string{"epic run blocked", "blocked", "ver-uqs5", "ver-abcd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error message to contain %q, got %q", want, msg)
		}
	}
}

// TestRunEpicArgErrorStillPrintsUsage verifies the acceptance criterion that
// argument errors from `verk run epic` continue to show the Cobra usage text.
// This guards against the new SilenceUsage flip leaking into the arg-validation
// path (which runs before RunE and therefore before the blocked-detection
// hook).
//
// Cobra writes the "Error:" prefix to stderr and the usage block to stdout
// (via c.Println), so the test inspects both streams: the operator sees them
// combined in the terminal but the separation matters for assertions here.
func TestRunEpicArgErrorStillPrintsUsage(t *testing.T) {
	stdout, stderr := osPipe(t)
	exit := ExecuteArgs([]string{"run", "epic"}, stdout, stderr)
	if exit == 0 {
		t.Fatal("expected non-zero exit for missing ticket-id arg")
	}
	_ = stdout.Sync()
	_ = stderr.Sync()
	combined := string(readFile(t, stdout.Name())) + string(readFile(t, stderr.Name()))
	if !strings.Contains(combined, "Usage:") {
		t.Fatalf("expected usage for arg error, got:\n%s", combined)
	}
	// Must also mention the failing command name so the operator knows what
	// the usage block applies to.
	if !strings.Contains(combined, "epic") {
		t.Fatalf("expected usage to mention 'epic', got:\n%s", combined)
	}
}

// readFile is a small helper that opens path read-only and returns its content.
// Defined locally so the test isn't sensitive to changes in a shared helper.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
