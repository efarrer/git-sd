package ui

import (
	"os"
	"strings"
	"testing"
)

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "abc", 3},
		{"em dash is one column", "\u2014", 1},
		{"box drawing is one column", "\u2514\u2500 sub", 6},
		{"emoji take two columns", "\u2705", 2},
		{"emoji then text", "\U0001F6A2 Merged", 9},
		{"colored text counts only visible characters", "\x1b[32mApproved\x1b[0m", 8},
		{"colored emoji", "\x1b[31m\U0001F6A8\x1b[0m x", 4},
		{"variation selector adds nothing", "a\ufe0f", 1},
		{"combining accent adds nothing", "e\u0301", 1},
		{"CJK is wide", "\u65e5\u672c", 4},
	}
	for _, c := range cases {
		if got := DisplayWidth(c.in); got != c.want {
			t.Errorf("%s: DisplayWidth(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestEveryMarkerIconHasTheWidthTheTableAssumes(t *testing.T) {
	for _, m := range Markers() {
		switch m.Icon {
		case "":
		case "\u2014":
			if DisplayWidth(m.Icon) != 1 {
				t.Errorf("%s: icon %q should be 1 column", m.ID, m.Icon)
			}
		default:
			if DisplayWidth(m.Icon) != 2 {
				t.Errorf("%s: icon %q should be 2 columns wide, got %d", m.ID, m.Icon, DisplayWidth(m.Icon))
			}
		}
	}
}

func TestFormatTableAlignsEmojiBlanksAndColor(t *testing.T) {
	rows := [][]string{
		{"\U0001F6A2", "Merged", "0", "feat-a"},
		{"\u2705", "\x1b[32mApproved\x1b[0m", "1", "feat-a"},
		{"", "Ready", "2", ""},
		{"\u2014", "Untracked", "10", "bugfix-b"},
		{"\U0001F7E1", "CI running", "3", "feat-c"},
	}
	out := FormatTable([]string{"", "Status", "Index", "Stack Set"}, rows)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("expected %d lines, got %d:\n%s", len(rows)+1, len(lines), out)
	}
	// Every line has the same display width, and the third column starts at
	// the same display column on every line.
	want := DisplayWidth(lines[0])
	for i, l := range lines {
		if got := DisplayWidth(l); got != want {
			t.Errorf("line %d is %d columns wide, want %d: %q", i, got, want, l)
		}
	}
	colStart := func(l string, cell string) int {
		idx := strings.Index(l, cell)
		if idx < 0 {
			t.Fatalf("%q not found in %q", cell, l)
		}
		return DisplayWidth(l[:idx])
	}
	wantStart := colStart(lines[0], "Index")
	for i, cell := range []string{"0", "1", "2", "10", "3"} {
		if got := colStart(lines[i+1], cell+" "); got != wantStart {
			t.Errorf("row %d: column 3 starts at %d, want %d: %q", i, got, wantStart, lines[i+1])
		}
	}
}

func TestFormatTableWithNoRowsIsEmpty(t *testing.T) {
	if out := FormatTable([]string{"a"}, nil); out != "" {
		t.Fatalf("expected no output for no rows, got %q", out)
	}
}

func TestMarkersAreUniqueAndComplete(t *testing.T) {
	icons, words, ids := map[string]MarkerID{}, map[string]MarkerID{}, map[MarkerID]bool{}
	for _, m := range Markers() {
		if ids[m.ID] {
			t.Errorf("duplicate marker id %s", m.ID)
		}
		ids[m.ID] = true
		if m.Word == "" || m.Text == "" || m.Group == "" {
			t.Errorf("%s: word, legend text and group are required: %+v", m.ID, m)
		}
		if other, dup := words[m.Word]; dup {
			t.Errorf("markers %s and %s share the word %q", other, m.ID, m.Word)
		}
		words[m.Word] = m.ID
		if m.Icon != "" {
			if other, dup := icons[m.Icon]; dup {
				t.Errorf("markers %s and %s share the icon %q", other, m.ID, m.Icon)
			}
			icons[m.Icon] = m.ID
		}
		if got := MarkerFor(m.ID); got.Word != m.Word {
			t.Errorf("MarkerFor(%s) returned %+v", m.ID, got)
		}
	}
}

func TestLegendHasNoUrgencyLabelsAndExplainsPrecedence(t *testing.T) {
	out := LegendText()
	for _, bad := range []string{"Critical", "priority", "Intermediate", "Prioritized"} {
		if strings.Contains(out, bad) {
			t.Errorf("legend should not carry urgency labels, found %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, PrecedenceNote) {
		t.Errorf("legend should explain precedence with %q:\n%s", PrecedenceNote, out)
	}
}

func TestLegendIsGeneratedFromTheMarkerTable(t *testing.T) {
	out := LegendText()
	for _, m := range Markers() {
		if !strings.Contains(out, m.Word+" ") && strings.Contains(out, m.Text) {
			t.Errorf("legend is missing %s (%q)", m.ID, m.Word)
		}
		if m.Icon != "" && !strings.Contains(out, m.Icon) {
			t.Errorf("legend is missing the icon for %s", m.ID)
		}
		if m.Action != "" && !strings.Contains(out, "Action: "+m.Action) {
			t.Errorf("legend is missing the action for %s", m.ID)
		}
	}
}

// legendBlock extracts the legend text from a docs file: the lines between the
// opening marker and the next closing fence/heredoc terminator.
func legendBlock(t *testing.T, path, opener, closer string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(data)
	start := strings.Index(s, opener)
	if start < 0 {
		t.Fatalf("%s: could not find %q", path, opener)
	}
	start += len(opener)
	end := strings.Index(s[start:], closer)
	if end < 0 {
		t.Fatalf("%s: could not find the end of the legend block", path)
	}
	return s[start : start+end]
}

func TestSpecLegendMatchesTheProgram(t *testing.T) {
	got := legendBlock(t, "../../SPEC.md", "#### Legend Example\n```text\n", "```")
	if got != LegendText() {
		t.Fatalf("SPEC.md's legend example differs from `git sd status --legend`.\n--- SPEC.md ---\n%s\n--- program ---\n%s", got, LegendText())
	}
}

func TestSimulationLegendMatchesTheProgram(t *testing.T) {
	got := legendBlock(t, "../../sd-sim.sh", "show_legend() {\n    cat <<EOF\n", "EOF\n}")
	if got != LegendText() {
		t.Fatalf("sd-sim.sh's legend differs from `git sd status --legend`.\n--- sd-sim.sh ---\n%s\n--- program ---\n%s", got, LegendText())
	}
}

func TestColorize(t *testing.T) {
	cases := []struct {
		color Color
		want  string
	}{
		{ColorRed, "\x1b[31mCI failed\x1b[0m"},
		{ColorGreen, "\x1b[32mCI failed\x1b[0m"},
		{ColorYellow, "\x1b[33mCI failed\x1b[0m"},
		{ColorDim, "\x1b[2mCI failed\x1b[0m"},
		{ColorNone, "CI failed"},
	}
	for _, c := range cases {
		if got := Colorize(c.color, "CI failed"); got != c.want {
			t.Errorf("Colorize(%d) = %q, want %q", c.color, got, c.want)
		}
	}
	if got := Colorize(ColorRed, ""); got != "" {
		t.Errorf("an empty cell must stay empty (no stray escapes), got %q", got)
	}
}

func TestColorizedCellsAlignLikePlainOnes(t *testing.T) {
	plain := FormatTable([]string{"", "Status", "Index"}, [][]string{{"\U0001F6A8", "CI failed", "0"}, {"", "Ready", "1"}})
	colored := FormatTable([]string{"", "Status", "Index"}, [][]string{{"\U0001F6A8", Colorize(ColorRed, "CI failed"), "0"}, {"", Colorize(ColorDim, "Ready"), "1"}})
	strip := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == 0x1b {
				for i < len(s) && s[i] != 'm' {
					i++
				}
				continue
			}
			b.WriteByte(s[i])
		}
		return b.String()
	}
	if strip(colored) != plain {
		t.Errorf("coloring must not change the layout:\nplain:\n%s\ncolored (escapes removed):\n%s", plain, strip(colored))
	}
}

func TestColorEnabled(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		name       string
		configured string
		env        map[string]string
		terminal   bool
		want       bool
	}{
		{"a terminal, default config", "true", nil, true, true},
		{"unset config behaves like true", "", nil, true, true},
		{"not a terminal (piped or redirected)", "true", nil, false, false},
		{"configured off", "false", nil, true, false},
		{"NO_COLOR set", "true", map[string]string{"NO_COLOR": "1"}, true, false},
		{"NO_COLOR set to an empty string does not count", "true", map[string]string{"NO_COLOR": ""}, true, true},
		{"TERM=dumb", "true", map[string]string{"TERM": "dumb"}, true, false},
		{"another TERM is fine", "true", map[string]string{"TERM": "xterm-256color"}, true, true},
		{"not a terminal wins over everything", "true", map[string]string{"TERM": "xterm"}, false, false},
	}
	for _, c := range cases {
		if got := ColorEnabled(c.configured, env(c.env), c.terminal); got != c.want {
			t.Errorf("%s: ColorEnabled = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEveryMarkerHasAColorChoice(t *testing.T) {
	for _, m := range Markers() {
		colored := Colorize(m.Color, m.Word)
		if (m.Color == ColorNone) != (colored == m.Word) {
			t.Errorf("%s: color %d produced %q", m.ID, m.Color, colored)
		}
	}
}
