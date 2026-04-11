package tui

import (
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"verk/internal/engine"
)

// RunProgress starts the appropriate progress display for a verk run.
// Uses Bubble Tea TUI if stdout is a terminal, plain log output otherwise.
func RunProgress(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	if isTerminal() {
		return runTUI(runID, ch)
	}
	RunPlainProgress(w, ch)
	return nil
}

func runTUI(runID string, ch <-chan engine.ProgressEvent) error {
	m := NewRunModel(runID, ch)
	p := tea.NewProgram(m,
		tea.WithInput(nil), // no keyboard input — prevents terminal capability queries that leak escape sequences
	)
	_, err := p.Run()
	return err
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
