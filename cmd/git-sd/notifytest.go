package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/ui"
)

// notifyVariable is one environment variable sd.watch.notify-command receives.
type notifyVariable struct {
	Name    string
	Meaning string
	PerPR   bool // set only for a change to a PR (not for the offline notice)
}

// notifyVariables is the single list of what the notify command receives: the
// event builders in watch.go follow it, and `notify-test`, `--agent` and the
// manual all show it, so the documentation cannot drift from the code.
var notifyVariables = []notifyVariable{
	{"GIT_SD_TITLE", "a short headline, for a notification title", false},
	{"GIT_SD_MESSAGE", "a sentence naming the PR and what happened, with its title", false},
	{"GIT_SD_EVENT", "what kind of change it is (see 'git sd notify-test --list')", false},
	{"GIT_SD_STACK_SET", "the Stack Set's name", true},
	{"GIT_SD_POSITION", "the PR's 1-based place among its Stack Set's PRs, in stack order", true},
	{"GIT_SD_SET_SIZE", "how many PRs the Stack Set has", true},
	{"GIT_SD_PR_NUMBER", "the PR's number", true},
	{"GIT_SD_PR_URL", "the PR's URL", true},
	{"GIT_SD_PR_TITLE", "the PR's title", true},
	{"GIT_SD_FROM", "the state it was in (a Status word such as Review)", true},
	{"GIT_SD_TO", "the state it is in now", true},
}

// notifyEventKind describes one GIT_SD_EVENT value.
type notifyEventKind struct {
	Kind string
	When string
}

// notifyEventKinds lists every GIT_SD_EVENT value (a test checks that
// eventKind can produce nothing else).
var notifyEventKinds = []notifyEventKind{
	{"ci-running", "CI started on a PR"},
	{"ci-passed", "CI finished without failing and the PR moved on (for example to Review or Approved)"},
	{"ci-failed", "CI failed"},
	{"approved", "the PR was approved"},
	{"changes-requested", "a reviewer requested changes"},
	{"conflict", "the PR has a merge conflict or base mismatch"},
	{"merged", "the PR was merged"},
	{"closed", "the PR was closed without merging"},
	{"draft", "the PR was marked as a draft"},
	{"ready-for-review", "a draft PR was marked ready for review"},
	{"state-changed", "any other change of state"},
	{"offline", "GitHub could not be reached for three checks in a row (no PR; only the first three variables are set)"},
}

// notifyExamples are ready-to-paste desktop-notification settings. The command
// runs through `sh -c` with the variables in its environment, so each example
// reads them as variables, never pasted into a quoted string (a PR title with
// a quote in it must not break the command).
const notifyExamples = `macOS (built in; no install):
  git config sd.watch.notify-command 'osascript -e "on run argv" -e "display notification (item 1 of argv) with title (item 2 of argv)" -e "end run" "$GIT_SD_MESSAGE" "$GIT_SD_TITLE"'

Linux (libnotify, preinstalled on most desktops):
  git config sd.watch.notify-command 'notify-send "$GIT_SD_TITLE" "$GIT_SD_MESSAGE"'

Windows (type these in Git Bash, which provides the 'sh' git-sd needs; install the
BurntToast module once with: powershell -Command "Install-Module BurntToast"):
  git config sd.watch.notify-command "powershell.exe -NoProfile -Command 'New-BurntToastNotification -Text \$env:GIT_SD_TITLE, \$env:GIT_SD_MESSAGE'"
  (without BurntToast, on Windows Pro editions: git config sd.watch.notify-command 'msg * "$GIT_SD_MESSAGE"')

Use 'git config --global' to set it for every repository.`

// notifyGitHubStates are the states a PR can change between: the ones GitHub
// reports (the local-only markers never notify).
var notifyGitHubStates = []ui.MarkerID{ui.Review, ui.Draft, ui.CIRunning, ui.CIFailed, ui.Changes, ui.Conflict, ui.Approved, ui.Merged, ui.Closed}

// samplePR is the made-up PR a test notification is about.
var samplePR = watchedPR{
	Number:   123,
	URL:      "https://github.com/example/repo/pull/123",
	StackSet: "demo",
	Title:    "Example change",
	Position: 2,
	SetSize:  3,
}

type notifyTestOptions struct {
	From, To ui.MarkerID
	Offline  bool
	List     bool
}

func notifyStateKey(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s))
}

// notifyStateNames lists the accepted state words, for error messages.
func notifyStateNames() string {
	var words []string
	for _, id := range notifyGitHubStates {
		words = append(words, ui.MarkerFor(id).Word)
	}
	return strings.Join(words, ", ")
}

// parseNotifyState reads a state given as its Status word (any case; spaces,
// hyphens or underscores are ignored, so "CI running" and "ci-running" both work).
func parseNotifyState(flag, value string) (ui.MarkerID, error) {
	key := notifyStateKey(value)
	for _, id := range notifyGitHubStates {
		if notifyStateKey(ui.MarkerFor(id).Word) == key {
			return id, nil
		}
	}
	return "", fmt.Errorf("unknown state '%s' for %s (valid: %s)", value, flag, notifyStateNames())
}

// parseNotifyTestArgs reads the flags of `git sd notify-test`.
func parseNotifyTestArgs(args []string) (notifyTestOptions, error) {
	var o notifyTestOptions
	var haveFrom, haveTo bool
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--list":
			o.List = true
		case "--offline":
			o.Offline = true
		case "--from", "--to":
			if !hasValue {
				if i+1 >= len(args) {
					return o, fmt.Errorf("%s needs a state (one of: %s)", name, notifyStateNames())
				}
				i++
				value = args[i]
			}
			id, err := parseNotifyState(name, value)
			if err != nil {
				return o, err
			}
			if name == "--from" {
				o.From, haveFrom = id, true
			} else {
				o.To, haveTo = id, true
			}
		default:
			return o, fmt.Errorf("unknown argument '%s' (usage: git sd notify-test [--from <state>] [--to <state>] [--offline] [--list])", arg)
		}
	}
	if o.Offline && (haveFrom || haveTo) {
		return o, fmt.Errorf("--offline is not a change to a PR, so it cannot be combined with --from or --to")
	}
	if !haveFrom {
		o.From = ui.Review
		if haveTo && o.To == ui.Review {
			o.From = ui.Draft
		}
	}
	if !haveTo {
		o.To = ui.Approved
		if o.From == ui.Approved {
			o.To = ui.Merged
		}
	}
	if !o.Offline && o.From == o.To {
		return o, fmt.Errorf("--from and --to are both %s: that is not a change", ui.MarkerFor(o.From).Word)
	}
	return o, nil
}

// notifyEnvNames returns the names in env ("NAME=value"), in order.
func notifyEnvNames(env []string) []string {
	var names []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return names
}

func envValue(env []string, name string) string {
	v, _ := lookupEnv(env, name)
	return v
}

// notifyVariablesTable renders the variables, with this event's values when env is given.
func notifyVariablesTable(env []string) string {
	header := []string{"Variable", "Meaning"}
	if env != nil {
		header = []string{"Variable", "Value", "Meaning"}
	}
	var rows [][]string
	for _, v := range notifyVariables {
		if env != nil {
			if _, set := lookupEnv(env, v.Name); !set {
				continue
			}
			rows = append(rows, []string{v.Name, envValue(env, v.Name), v.Meaning})
			continue
		}
		rows = append(rows, []string{v.Name, v.Meaning})
	}
	return ui.FormatTable(header, rows)
}

func lookupEnv(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			return v, true
		}
	}
	return "", false
}

// notifyEventKindsTable renders every GIT_SD_EVENT value.
func notifyEventKindsTable() string {
	var rows [][]string
	for _, k := range notifyEventKinds {
		rows = append(rows, []string{k.Kind, k.When})
	}
	return ui.FormatTable([]string{"GIT_SD_EVENT", "When"}, rows)
}

// runNotifyCommandCapture is runNotifyCommand that also returns what the
// command printed.
func runNotifyCommandCapture(command string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), watchNotifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", watchNotifyTimeout)
	}
	return string(out), err
}

// runNotifyTest is `git sd notify-test`: it builds a sample notification the
// way `watch` does, shows the variables, and runs sd.watch.notify-command with
// them so the setting can be tried without waiting for a real change.
func runNotifyTest(args []string) {
	opts, err := parseNotifyTestArgs(args)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
	if opts.List {
		fmt.Print(notifyEventKindsTable())
		return
	}

	var env []string
	var what string
	if opts.Offline {
		env = messageEnv("offline", "git sd watch cannot reach GitHub; it will keep retrying")
		what = "the offline notice"
	} else {
		pr := samplePR
		pr.Marker = opts.To
		e := watchEvent{PR: pr, From: opts.From, To: opts.To, Kind: eventKind(opts.From, opts.To)}
		env = eventEnv(e)
		what = fmt.Sprintf("a sample change, %s → %s, to PR #%d", markerWord(opts.From), markerWord(opts.To), pr.Number)
	}
	fmt.Printf("Test notification: %s (GIT_SD_EVENT=%s)\n\n", what, envValue(env, "GIT_SD_EVENT"))
	fmt.Println("The command receives these environment variables:")
	fmt.Print(notifyVariablesTable(env))
	fmt.Println()

	command := strings.TrimSpace(git.ConfigGetWithDefault("sd.watch.notify-command", ""))
	if command == "" {
		fmt.Println("sd.watch.notify-command is not set, so nothing was run. Set it to a command that")
		fmt.Println("shows a notification, for example:")
		fmt.Println()
		fmt.Println(notifyExamples)
		return
	}
	if stdoutIsTerminal() {
		fmt.Print("\a") // watch rings the bell on a terminal for each cycle with changes
	}
	fmt.Printf("Running: %s\n", command)
	out, err := runNotifyCommandCapture(command, env)
	if out = strings.TrimRight(out, "\n"); out != "" {
		fmt.Println(out)
	}
	if err != nil {
		fmt.Printf("The command failed: %v\n", err)
		exitCleanly(1)
	}
	fmt.Println("The command succeeded.")
}
