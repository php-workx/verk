package tui

import (
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	term "github.com/charmbracelet/x/term"

	"verk/internal/engine"
)

// RunProgress starts the appropriate progress display for a verk run.
// Uses Bubble Tea TUI if the output writer is a terminal, plain log output otherwise.
func RunProgress(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	if isTerminal(w) {
		return runBubbleTea(runID, ch)
	}
	RunPlainProgress(w, ch)
	return nil
}

func runBubbleTea(runID string, ch <-chan engine.ProgressEvent) error {
	m := NewRunModel(runID, ch)
	p := tea.NewProgram(m,
		tea.WithInput(nil), // no keyboard input — prevents terminal capability query leaks
	)
	_, err := p.Run()

	// Drain any leftover terminal capability responses (CSI ?2026$p,
	// CSI ?2027$p, Kitty keyboard) that Bubble Tea v2's renderer probes
	// during startup. These arrive after the TUI exits and would otherwise
	// leak to stdout as garbage like "2026;2$y2027;1$y1u".
	drainTerminalResponses()

	return err
}

// drainTerminalResponses reads and discards any pending terminal response
// sequences from stdin. Bubble Tea v2 sends DECRQM queries on startup
// (mode 2026 for synchronized output, mode 2027 for unicode core, and
// Kitty keyboard protocol). The terminal's responses can arrive after
// the TUI exits, causing escape sequence garbage on the command line.
func drainTerminalResponses() {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return
	}
	// Set stdin to non-blocking raw mode briefly to drain responses.
	// The DECRQM responses arrive almost instantly from the terminal.
	oldState, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return
	}
	// Set a short read deadline so we don't block if there's nothing to drain.
	// We use a goroutine with a timeout since os.File doesn't support deadlines
	// on all platforms. A cancel channel is closed before restoring the terminal
	// so the goroutine stops looping as soon as its current Read unblocks.
	cancel := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			// Check for cancellation before attempting another read so the
			// goroutine terminates promptly once the timeout fires.
			select {
			case <-cancel:
				return
			default:
			}
			if _, err := os.Stdin.Read(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
	}
	close(cancel) // Signal goroutine to stop on its next iteration
	_ = term.Restore(os.Stdin.Fd(), oldState)
}

func isTerminal(w io.Writer) bool {
	type fd interface{ Fd() uintptr }
	if f, ok := w.(fd); ok {
		return term.IsTerminal(f.Fd())
	}
	// Fallback for writers without a file descriptor: check os.Stdout as a heuristic
	// only if the writer IS os.Stdout (e.g. when called without an explicit writer).
	if f, ok := w.(*os.File); ok && f == os.Stdout {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return fi.Mode()&os.ModeCharDevice != 0
	}
	return false
}
