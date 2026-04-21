package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template" // nosemgrep: go.lang.security.audit.xss.import-text-template.import-text-template -- Cobra renders terminal help, not HTML.
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	maxBoxWidth = 72
	minBoxWidth = 40
)

var groupCommandOrder = map[string][]string{
	groupExecution: {"init", "run", "reopen"},
	groupObserve:   {"status", "doctor"},
}

var shouldColorizeFunc = shouldColorize

func initHelp(root *cobra.Command) {
	cobra.AddTemplateFuncs(template.FuncMap{
		"groupedHelp": groupedHelp,
	})
	root.SetUsageTemplate(usageTemplate)
}

func shouldColorize() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type helpRenderer struct{ color bool }

func (r helpRenderer) dim(s string) string {
	if !r.color {
		return s
	}
	return styleDim.Render(s)
}

func (r helpRenderer) bold(s string) string {
	if !r.color {
		return s
	}
	return styleBold.Render(s)
}

func (r helpRenderer) titleStyle(s string) string {
	if !r.color {
		return s
	}
	return styleBoldCyan.Render(s)
}

func boxTopFill(title string, width int) int {
	inner := width - 2
	titleW := utf8.RuneCountInString(title)
	fill := inner - 3 - titleW
	if fill < 0 {
		fill = 0
	}
	return fill
}

func (r helpRenderer) top(title string, width int) string {
	fill := boxTopFill(title, width)
	filling := " " + strings.Repeat("─", fill) + "╮"
	return r.dim("╭─ ") + r.titleStyle(title) + r.dim(filling)
}

func boxRowLayout(name, short string, namePad, width int) (nameField, desc string, pad int) {
	inner := width - 2
	available := inner - 1

	nameField = fmt.Sprintf("%-*s", namePad, name)
	prefixW := 2 + utf8.RuneCountInString(nameField) + 1
	maxDescW := available - prefixW

	desc = short
	if utf8.RuneCountInString(short) > maxDescW {
		switch {
		case maxDescW > 3:
			desc = string([]rune(short)[:maxDescW-3]) + "..."
		case maxDescW > 0:
			desc = string([]rune(short)[:maxDescW])
		default:
			desc = ""
		}
	}

	contentW := prefixW + utf8.RuneCountInString(desc)
	pad = available - contentW
	if pad < 0 {
		pad = 0
	}
	return nameField, desc, pad
}

func (r helpRenderer) row(name, short string, namePad, width int) string {
	nameField, desc, pad := boxRowLayout(name, short, namePad, width)
	return r.dim("│") + "  " + r.bold(nameField) + " " +
		r.dim(desc+strings.Repeat(" ", pad)+" │")
}

func (r helpRenderer) bottom(width int) string {
	inner := width - 2
	return r.dim("╰" + strings.Repeat("─", inner) + "╯")
}

func groupedHelp(cmd *cobra.Command) string {
	groups := cmd.Groups()
	if len(groups) == 0 {
		return ""
	}

	w := boxWidth()
	namePad := cmd.NamePadding()
	r := helpRenderer{color: shouldColorizeFunc()}
	var b strings.Builder

	for _, group := range groups {
		cmds := commandsForGroup(cmd, group.ID)
		if len(cmds) == 0 {
			continue
		}
		sortByGroupOrder(cmds, group.ID)
		renderGroupSection(&b, r, group.Title, cmds, namePad, w)
	}

	renderUngroupedSection(&b, r, cmd, namePad)
	return b.String()
}

func commandsForGroup(cmd *cobra.Command, groupID string) []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.GroupID == groupID && c.IsAvailableCommand() {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func renderGroupSection(b *strings.Builder, r helpRenderer, title string, cmds []*cobra.Command, namePad, w int) {
	if w < minBoxWidth {
		b.WriteString(r.titleStyle(title) + ":\n")
		for _, c := range cmds {
			nameField := fmt.Sprintf("%-*s", namePad, c.Name())
			fmt.Fprintf(b, "  %s %s\n", r.bold(nameField), r.dim(c.Short))
		}
		b.WriteByte('\n')
	} else {
		b.WriteString(r.top(title, w))
		b.WriteByte('\n')
		for _, c := range cmds {
			b.WriteString(r.row(c.Name(), c.Short, namePad, w))
			b.WriteByte('\n')
		}
		b.WriteString(r.bottom(w))
		b.WriteByte('\n')
	}
}

func renderUngroupedSection(b *strings.Builder, r helpRenderer, cmd *cobra.Command, namePad int) {
	var ungrouped []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.GroupID == "" && (c.IsAvailableCommand() || c.Name() == "help") {
			ungrouped = append(ungrouped, c)
		}
	}
	if len(ungrouped) > 0 {
		b.WriteString("\n" + r.dim("Additional Commands:") + "\n")
		for _, c := range ungrouped {
			nameField := fmt.Sprintf("%-*s", namePad, c.Name())
			fmt.Fprintf(b, "  %s %s\n", r.bold(nameField), r.dim(c.Short))
		}
	}
}

func boxWidth() int {
	return maxBoxWidth
}

func sortByGroupOrder(cmds []*cobra.Command, groupID string) {
	order, ok := groupCommandOrder[groupID]
	if !ok {
		return
	}
	rank := make(map[string]int, len(order))
	for i, name := range order {
		rank[name] = i
	}
	sort.SliceStable(cmds, func(i, j int) bool {
		ri, oki := rank[cmds[i].Name()]
		rj, okj := rank[cmds[j].Name()]
		if oki && okj {
			return ri < rj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		return cmds[i].Name() < cmds[j].Name()
	})
}

const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}

{{groupedHelp .}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}
Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
