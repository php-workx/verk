package tui

import (
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"verk/internal/engine"
)

// RunProgress starts the appropriate progress display for a verk run.
// Currently uses plain log output. Bubble Tea TUI is available via RunTUI
// but needs more testing before becoming the default.
func RunProgress(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	RunPlainProgress(w, ch)
	return nil
}

// RunTUI starts the Bubble Tea TUI for run progress.
// Use this when you want the interactive updating display.
func RunTUI(runID string, ch <-chan engine.ProgressEvent) error {
	return runTUI(runID, ch)
}

func runTUI(runID string, ch <-chan engine.ProgressEvent) error {
	m := NewRunModel(runID, ch)
	p := tea.NewProgram(m)
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
