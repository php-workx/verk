package tui

import (
	"errors"
	"io"
	"os"
	"syscall"
	"time"
	"verk/internal/engine"

	tea "charm.land/bubbletea/v2"
	term "github.com/charmbracelet/x/term"
)

// RunProgress starts the appropriate progress display for a verk run.
// Uses Bubble Tea TUI if the output writer is a terminal, plain log output otherwise.
func RunProgress(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	return RunProgressWithCancel(runID, ch, w, nil)
}

// RunProgressWithCancel starts the appropriate progress display for a verk run
// and invokes onCancel when an interactive operator presses a cancellation key.
func RunProgressWithCancel(runID string, ch <-chan engine.ProgressEvent, w io.Writer, onCancel func()) error {
	if isTerminal(w) {
		return runBubbleTea(runID, ch, onCancel)
	}
	RunPlainProgress(w, ch)
	return nil
}

func runBubbleTea(runID string, ch <-chan engine.ProgressEvent, onCancel func()) error {
	m := NewRunModelWithCancel(runID, ch, onCancel)
	opts := []tea.ProgramOption{}
	if !term.IsTerminal(os.Stdin.Fd()) {
		opts = append(opts, tea.WithInput(nil))
	}
	p := tea.NewProgram(m, opts...)
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
//
// The drain is fully synchronous: stdin is switched into non-blocking mode,
// read until it reports EAGAIN (or a short budget elapses), then restored.
// No background goroutine survives this call, so a subsequent reader on
// stdin (e.g. the reopen-retry prompt) cannot race us for the first byte.
func drainTerminalResponses() {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return
	}
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(uintptr(fd))
	if err != nil {
		return
	}
	defer func() { _ = term.Restore(uintptr(fd), oldState) }()

	if err := syscall.SetNonblock(fd, true); err != nil {
		return
	}
	defer func() { _ = syscall.SetNonblock(fd, false) }()

	// Response bytes typically arrive within a few milliseconds; 50ms keeps
	// parity with the previous budget and stays well below any interactive
	// delay a human operator would notice.
	deadline := time.Now().Add(50 * time.Millisecond)
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := syscall.Read(fd, buf)
		if n > 0 {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			// Nothing pending right now; wait a beat for in-flight responses.
			time.Sleep(2 * time.Millisecond)
			continue
		}
		// Any other error (EBADF, EIO, …) leaves stdin in a state we can't
		// usefully drain; stop rather than spinning.
		return
	}
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
