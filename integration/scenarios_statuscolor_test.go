package integration

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes color escape sequences.
func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// sdOnTerminal runs git-sd under a pseudo-terminal (via script(1)) so that
// standard output really is a terminal, with the fake gh first on PATH. extraEnv
// is added to the environment (TERM defaults to xterm).
func (e *batchPushEnv) sdOnTerminal(t *testing.T, extraEnv []string, args ...string) string {
	t.Helper()
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script(1) is not available to give git-sd a terminal")
	}
	cmdline := gitSDPath + " " + strings.Join(args, " ")
	cmd := exec.Command(scriptPath, "-qec", cmdline, "/dev/null") // util-linux
	if runtime.GOOS == "darwin" {                                 // BSD script
		cmd = exec.Command(scriptPath, append([]string{"-q", "/dev/null", gitSDPath}, args...)...)
	}
	cmd.Dir = e.repo
	env := append(os.Environ(), "PATH="+e.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"), "TERM=xterm")
	// A developer's own NO_COLOR must not leak into these tests.
	var clean []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "NO_COLOR=") {
			clean = append(clean, kv)
		}
	}
	cmd.Env = append(clean, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmdline, err, out)
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n")
}

// TestSDStatusColorsTheWordOnlyOnATerminal checks the whole color contract:
// colored on a terminal by default, plain when piped, and off for NO_COLOR,
// TERM=dumb and sd.status.color=false; only the word is colored; and colored
// rows still line up.
func TestSDStatusColorsTheWordOnlyOnATerminal(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if o, err := e.sd("assign", "tmp:0"); err != nil { // B stays untracked-then-tracked below
		t.Fatalf("assign: %v\n%s", err, o)
	}
	if o, err := e.sd("sync"); err != nil { // the fake gh reports the PR open and unreviewed: Review (yellow)
		t.Fatalf("sync: %v\n%s", err, o)
	}
	const yellowReview = "\x1b[33mReview\x1b[0m"

	// Piped (not a terminal): never colored, whatever the config says.
	piped, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, piped)
	}
	if strings.Contains(piped, "\x1b[") {
		t.Errorf("piped output must carry no escape sequences:\n%q", piped)
	}
	e.git(e.repo, "config", "sd.status.color", "true")
	if piped, _ = e.sd("status"); strings.Contains(piped, "\x1b[") {
		t.Errorf("sd.status.color=true must not force color onto a pipe:\n%q", piped)
	}
	e.git(e.repo, "config", "--unset", "sd.status.color")

	// On a terminal: colored by default.
	term := e.sdOnTerminal(t, nil, "status")
	if !strings.Contains(term, yellowReview) {
		t.Fatalf("on a terminal the Review word should be yellow:\n%q", term)
	}
	if !strings.Contains(term, "\x1b[2mUntracked") && !strings.Contains(term, "\x1b[2mReady") {
		t.Errorf("the unassigned commit's word should be dim:\n%q", term)
	}
	// Only the word: the icon is never wrapped, and rows still align.
	rowA := stripANSI(rowWith(t, term, "Commit A"))
	rowB := stripANSI(rowWith(t, term, "Commit B"))
	if got := statusCell(t, term, rowA, "Index"); got != "0" {
		t.Errorf("colored row A: Index = %q, want 0:\n%q", got, term)
	}
	if got := statusCell(t, term, rowB, "Index"); got != "1" {
		t.Errorf("colored row B: Index = %q, want 1 (colors must not shift columns)", got)
	}
	if got := statusCell(t, term, rowA, "Status"); got != "Review" {
		t.Errorf("colored row A: Status = %q", got)
	}
	if strings.Contains(term, "\x1b[33m\U0001F440") || strings.Contains(term, "\x1b[2m\U0001F4E6") {
		t.Errorf("the icon must not be colored")
	}
	fmt.Println("status color verified on a terminal")

	// Off switches.
	for name, extra := range map[string][]string{
		"NO_COLOR":  {"NO_COLOR=1"},
		"TERM=dumb": {"TERM=dumb"},
	} {
		if out := e.sdOnTerminal(t, extra, "status"); strings.Contains(out, "\x1b[") {
			t.Errorf("%s must turn color off:\n%q", name, out)
		}
	}
	e.git(e.repo, "config", "sd.status.color", "false")
	if out := e.sdOnTerminal(t, nil, "status"); strings.Contains(out, "\x1b[") {
		t.Errorf("sd.status.color=false must turn color off:\n%q", out)
	}
}
