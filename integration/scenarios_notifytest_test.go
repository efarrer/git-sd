package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runNotifyTestCmd runs `git sd notify-test` in a directory that is NOT a git
// repository, with an empty home whose git config sets sd.watch.notify-command
// to command (when not empty): the command needs nothing but git config.
func runNotifyTestCmd(t *testing.T, command string, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	if command != "" {
		cfg := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "sd.watch.notify-command", command)
		if out, err := cfg.CombinedOutput(); err != nil {
			t.Fatalf("git config: %v\n%s", err, out)
		}
	}
	cmd := exec.Command(gitSDPath, append([]string{"notify-test"}, args...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "xdg"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func TestSDNotifyTestExplainsAndRunsTheCommand(t *testing.T) {
	t.Parallel()

	// No command set: explains the variables and shows desktop examples, exit 0.
	out, err := runNotifyTestCmd(t, "")
	if err != nil {
		t.Fatalf("notify-test without a command is not an error: %v\n%s", err, out)
	}
	for _, want := range []string{"Review → Approved", "GIT_SD_EVENT=approved", "GIT_SD_MESSAGE", "demo 2/3: Approved",
		"https://github.com/example/repo/pull/123", "is not set, so nothing was run", "osascript", "notify-send", "BurntToast"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// A command set: it receives every variable, with the chosen change.
	dump := filepath.Join(t.TempDir(), "env.txt")
	out, err = runNotifyTestCmd(t, "env | grep -E '^GIT_SD_' | grep -v '^GIT_SD_TEST_' | sort > "+dump+"; echo notified", "--from", "approved", "--to", "merged")
	if err != nil {
		t.Fatalf("notify-test with a working command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Running: env | grep") || !strings.Contains(out, "notified") || !strings.Contains(out, "The command succeeded.") {
		t.Errorf("output should show the command, what it printed and that it succeeded:\n%s", out)
	}
	got, _ := os.ReadFile(dump)
	for _, want := range []string{"GIT_SD_EVENT=merged", "GIT_SD_FROM=Approved", "GIT_SD_TO=Merged", "GIT_SD_PR_NUMBER=123",
		"GIT_SD_STACK_SET=demo", "GIT_SD_POSITION=2", "GIT_SD_SET_SIZE=3", "GIT_SD_PR_TITLE=Example change",
		"GIT_SD_PR_URL=https://github.com/example/repo/pull/123", "GIT_SD_TITLE=demo 2/3: Merged", "GIT_SD_MESSAGE=PR 2 of 3 in demo (#123) was merged: Example change"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the command's environment lacks %q:\n%s", want, got)
		}
	}

	// The offline notice has no PR: only three variables.
	out, err = runNotifyTestCmd(t, "env | grep -E '^GIT_SD_' | grep -v '^GIT_SD_TEST_' | sort > "+dump, "--offline")
	if err != nil {
		t.Fatalf("notify-test --offline: %v\n%s", err, out)
	}
	got, _ = os.ReadFile(dump)
	if lines := strings.Split(strings.TrimSpace(string(got)), "\n"); len(lines) != 3 || !strings.Contains(string(got), "GIT_SD_EVENT=offline") {
		t.Errorf("the offline notice sets exactly GIT_SD_TITLE, GIT_SD_MESSAGE and GIT_SD_EVENT=offline, got:\n%s", got)
	}

	// A failing command is reported, with its output, and the exit status is 1.
	out, err = runNotifyTestCmd(t, "echo boom; exit 3")
	if err == nil || !strings.Contains(out, "boom") || !strings.Contains(out, "The command failed") {
		t.Errorf("a failing command is reported and fails notify-test: %v\n%s", err, out)
	}

	// --list needs nothing configured and names every event kind.
	out, err = runNotifyTestCmd(t, "", "--list")
	if err != nil || !strings.Contains(out, "ready-for-review") || !strings.Contains(out, "changes-requested") {
		t.Errorf("--list prints the event kinds: %v\n%s", err, out)
	}

	// Bad input is an error that says what is valid.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "nope"}, "unknown state 'nope' for --from (valid: Review, Draft, CI running"},
		{[]string{"--from", "approved", "--to", "approved"}, "that is not a change"},
		{[]string{"--offline", "--to", "merged"}, "cannot be combined"},
		{[]string{"extra"}, "unknown argument 'extra'"},
	} {
		out, err = runNotifyTestCmd(t, "", c.args...)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("notify-test %v should fail with %q: %v\n%s", c.args, c.want, err, out)
		}
	}
}

// `git-sd --help` and `git-sd help` print the whole manual; -h the short usage.
// (`git sd --help` itself never reaches the binary: git looks for a man page.)
func TestSDHelpPrintsTheManual(t *testing.T) {
	t.Parallel()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitSDPath, args...)
		cmd.Dir = t.TempDir() // not a repository: help needs none
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git-sd %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	manual, viaHelp := run("--help"), run("help")
	if manual != viaHelp {
		t.Errorf("--help and help print the same manual")
	}
	for _, want := range []string{"NAME\n", "COMMANDS", "notify-test", "GIT_SD_TITLE", "BurntToast", "sd.watch.max-interval", "EXIT STATUS"} {
		if !strings.Contains(manual, want) {
			t.Errorf("the manual lacks %q", want)
		}
	}
	if short := run("-h"); !strings.HasPrefix(short, "usage: git sd <command>") || len(short) >= len(manual) {
		t.Errorf("-h is the short usage, got:\n%.200s", short)
	}
	if strings.Contains(manual, "SPEC.md") || strings.Contains(manual, "man page") && strings.Contains(manual, "Open this") {
		t.Errorf("the manual must stand on its own")
	}
}

// `git sd track` was removed: assign, move and fixup tag commits themselves.
func TestSDTrackIsGone(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(gitSDPath, "track")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Unknown command: track") {
		t.Errorf("track is an unknown command: %v\n%s", err, out)
	}
}
