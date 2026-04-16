package tui

import "charm.land/lipgloss/v2"

var (
	styleHeader    = lipgloss.NewStyle().Bold(true)
	styleDivider   = lipgloss.NewStyle().Faint(true)
	styleWaveTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))

	styleTicketID    = lipgloss.NewStyle().Bold(true)
	styleTicketTitle = lipgloss.NewStyle().Faint(true)

	stylePhaseChain = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	stylePhaseDone  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stylePhaseFail  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	stylePhaseWait  = lipgloss.NewStyle().Faint(true)
	styleActive     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	styleElapsed = lipgloss.NewStyle().Faint(true)

	styleDetailDim = lipgloss.NewStyle().Faint(true)

	styleWaveSummaryOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleWaveSummaryFail = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)
