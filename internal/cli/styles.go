package cli

import "charm.land/lipgloss/v2"

var (
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleBoldCyan = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))

	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	styleFail = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
)
