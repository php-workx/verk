package tui

import (
	"io"
	"os"

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
	return err
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
