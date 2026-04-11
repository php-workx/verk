package tui

import "charm.land/lipgloss/v2"

var (
	styleRunHeader = lipgloss.NewStyle().Bold(true)
	styleWaveHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleDivider   = lipgloss.NewStyle().Faint(true)

	styleTicketID = lipgloss.NewStyle().Bold(true)
	styleTitle    = lipgloss.NewStyle().Faint(true)

	stylePhaseActive = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	stylePhaseDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	stylePhaseFail   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	stylePhaseWait   = lipgloss.NewStyle().Faint(true)

	styleDetailLine = lipgloss.NewStyle().Faint(true)

	styleCheckOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleCheckFail = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleCheckWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)
