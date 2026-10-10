package ui

import (
	"fmt"
	"strings"
)

// FormatTable lays out rows under columns, padding every cell to its column's
// widest DISPLAY width (see DisplayWidth) so emoji, blanks and colored words
// stay aligned.
func FormatTable(columns []string, rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}

	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = DisplayWidth(col)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], DisplayWidth(cell))
			}
		}
	}

	var b strings.Builder
	line := func(cells []string) {
		for i, cell := range cells {
			if i < len(widths) {
				b.WriteString(cell)
				b.WriteString(strings.Repeat(" ", widths[i]-DisplayWidth(cell)))
				b.WriteString("  ")
			}
		}
		b.WriteString("\n")
	}
	line(columns)
	for _, row := range rows {
		line(row)
	}
	return b.String()
}

// PrintTable prints FormatTable's output.
func PrintTable(columns []string, rows [][]string) {
	fmt.Print(FormatTable(columns, rows))
}

// PrecedenceNote explains how a commit's single PR-state marker is chosen.
const PrecedenceNote = "Each commit shows the first state below that applies; Merged and Closed always win."

// LegendText renders the legend from the marker table: an icon column, a
// Status word column (the word the status table's Status column shows), then
// the meaning.
func LegendText() string {
	wordWidth := 0
	for _, m := range markers {
		wordWidth = max(wordWidth, DisplayWidth(m.Word))
	}
	var b strings.Builder
	group := ""
	for _, m := range markers {
		if m.Group != group {
			if group != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s:\n", m.Group)
			if m.Group == groupPR {
				b.WriteString("  " + PrecedenceNote + "\n")
			}
			group = m.Group
		}
		icon := m.Icon + strings.Repeat(" ", 2-DisplayWidth(m.Icon))
		word := m.Word + strings.Repeat(" ", wordWidth-DisplayWidth(m.Word))
		fmt.Fprintf(&b, "  %s  %s  %s\n", icon, word, m.Text)
		if m.Action != "" {
			fmt.Fprintf(&b, "  %s  %s  Action: %s\n", strings.Repeat(" ", 2), strings.Repeat(" ", wordWidth), m.Action)
		}
	}
	return b.String()
}

// PrintLegend prints the status legend.
func PrintLegend() {
	fmt.Print(LegendText())
}
