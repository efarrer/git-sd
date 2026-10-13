package main

import (
	"git-sd/internal/ui"
	"os"
	"slices"
	"strings"
	"testing"
)

func sampleEvent(from, to ui.MarkerID) watchEvent {
	pr := samplePR
	pr.Marker = to
	return watchEvent{PR: pr, From: from, To: to, Kind: eventKind(from, to)}
}

// The documented variables and the environment watch builds are the same list.
func TestNotifyVariablesMatchTheEnvironmentWatchBuilds(t *testing.T) {
	var all, general []string
	for _, v := range notifyVariables {
		all = append(all, v.Name)
		if !v.PerPR {
			general = append(general, v.Name)
		}
	}
	if got := notifyEnvNames(eventEnv(sampleEvent(ui.Review, ui.Approved))); !slices.Equal(got, all) {
		t.Errorf("a PR change sets %v, but notifyVariables lists %v", got, all)
	}
	if got := notifyEnvNames(messageEnv("offline", "x")); !slices.Equal(got, general) {
		t.Errorf("the offline notice sets %v, but notifyVariables marks %v as not per-PR", got, general)
	}
	for _, v := range notifyVariables {
		if !strings.HasPrefix(v.Name, "GIT_SD_") || v.Meaning == "" {
			t.Errorf("variable %+v needs a GIT_SD_ name and a meaning", v)
		}
	}
}

// Every GIT_SD_EVENT value eventKind can return is documented, and nothing
// else is.
func TestEveryEventKindIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, k := range notifyEventKinds {
		if documented[k.Kind] {
			t.Errorf("event kind %q is listed twice", k.Kind)
		}
		documented[k.Kind] = true
		if k.When == "" {
			t.Errorf("event kind %q has no description", k.Kind)
		}
	}
	produced := map[string]bool{"offline": true}
	for _, from := range notifyGitHubStates {
		for _, to := range notifyGitHubStates {
			if from == to {
				continue
			}
			kind := eventKind(from, to)
			produced[kind] = true
			if !documented[kind] {
				t.Errorf("%s -> %s gives event kind %q, which notifyEventKinds does not list", from, to, kind)
			}
		}
	}
	for kind := range documented {
		if !produced[kind] {
			t.Errorf("notifyEventKinds lists %q, which nothing produces", kind)
		}
	}
}

func TestParseNotifyTestArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		from, to ui.MarkerID
		offline  bool
		list     bool
		err      string
	}{
		{"defaults", nil, ui.Review, ui.Approved, false, false, ""},
		{"approved to merged", []string{"--from", "approved", "--to", "merged"}, ui.Approved, ui.Merged, false, false, ""},
		{"equals form", []string{"--from=Draft", "--to=CI running"}, ui.Draft, ui.CIRunning, false, false, ""},
		{"words are forgiving", []string{"--from", "ci-running", "--to", "CI_FAILED"}, ui.CIRunning, ui.CIFailed, false, false, ""},
		{"only --to", []string{"--to", "closed"}, ui.Review, ui.Closed, false, false, ""},
		{"only --from approved goes to merged", []string{"--from", "approved"}, ui.Approved, ui.Merged, false, false, ""},
		{"--to review starts from draft", []string{"--to", "review"}, ui.Draft, ui.Review, false, false, ""},
		{"offline", []string{"--offline"}, ui.Review, ui.Approved, true, false, ""},
		{"list", []string{"--list"}, ui.Review, ui.Approved, false, true, ""},
		{"unknown state", []string{"--from", "nope"}, "", "", false, false, "unknown state 'nope' for --from (valid: Review, Draft, CI running, CI failed, Changes, Conflict, Approved, Merged, Closed)"},
		{"a local-only marker is not a change", []string{"--to", "unsynced"}, "", "", false, false, "unknown state 'unsynced'"},
		{"same state", []string{"--from", "approved", "--to", "approved"}, "", "", false, false, "that is not a change"},
		{"missing value", []string{"--from"}, "", "", false, false, "--from needs a state"},
		{"offline with a change", []string{"--offline", "--to", "merged"}, "", "", false, false, "cannot be combined"},
		{"unknown argument", []string{"--nope"}, "", "", false, false, "unknown argument '--nope'"},
	}
	for _, c := range cases {
		o, err := parseNotifyTestArgs(c.args)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: error %v, want one containing %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if o.From != c.from || o.To != c.to || o.Offline != c.offline || o.List != c.list {
			t.Errorf("%s: got %+v, want from %s to %s offline %v list %v", c.name, o, c.from, c.to, c.offline, c.list)
		}
	}
}

// The sample notification is built by the same functions watch uses, so what
// notify-test shows is what a real change sends.
func TestSampleNotificationReadsLikeARealOne(t *testing.T) {
	env := eventEnv(sampleEvent(ui.Approved, ui.Merged))
	want := map[string]string{
		"GIT_SD_TITLE":     "demo 2/3: Merged",
		"GIT_SD_MESSAGE":   "PR 2 of 3 in demo (#123) was merged: Example change",
		"GIT_SD_EVENT":     "merged",
		"GIT_SD_STACK_SET": "demo",
		"GIT_SD_POSITION":  "2",
		"GIT_SD_SET_SIZE":  "3",
		"GIT_SD_PR_NUMBER": "123",
		"GIT_SD_PR_URL":    "https://github.com/example/repo/pull/123",
		"GIT_SD_PR_TITLE":  "Example change",
		"GIT_SD_FROM":      "Approved",
		"GIT_SD_TO":        "Merged",
	}
	for name, value := range want {
		if got := envValue(env, name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if strings.Contains(samplePR.URL, "lvt") || !strings.HasPrefix(samplePR.URL, "https://github.com/example/") {
		t.Errorf("the sample data must be generic, got %q", samplePR.URL)
	}
}

func TestRunNotifyCommandCaptureShowsOutputAndFailure(t *testing.T) {
	env := eventEnv(sampleEvent(ui.Review, ui.Approved))
	out, err := runNotifyCommandCapture(`printf '%s|%s' "$GIT_SD_EVENT" "$GIT_SD_TO"`, env)
	if err != nil || out != "approved|Approved" {
		t.Errorf("got %q, %v", out, err)
	}
	out, err = runNotifyCommandCapture(`echo boom >&2; exit 3`, env)
	if err == nil || !strings.Contains(out, "boom") {
		t.Errorf("a failing command reports its output and error, got %q, %v", out, err)
	}
	// A PR title with a quote in it must not break a command that reads the environment.
	quoted := eventEnv(watchEvent{PR: watchedPR{Number: 1, StackSet: "s", Title: `Fix "quotes" and $(whoami)`, Position: 1, SetSize: 1}, From: ui.Review, To: ui.Approved, Kind: "approved"})
	out, err = runNotifyCommandCapture(`printf '%s' "$GIT_SD_PR_TITLE"`, quoted)
	if err != nil || out != `Fix "quotes" and $(whoami)` {
		t.Errorf("the title reaches the command verbatim, got %q, %v", out, err)
	}
}

// The tool describes itself: nothing the installed program prints or ships as
// its manual may send the reader to a project document they don't have.
func TestToolOutputIsSelfContained(t *testing.T) {
	man, err := os.ReadFile("../../git-sd.1")
	if err != nil {
		t.Fatal(err)
	}
	docs := []string{"SPEC.md", "DESIGN.md", "TASKS.md", "BUGS.md", "DEVELOPMENT.md", "the SPEC"}
	texts := map[string]string{"git sd (usage)": usageText(), "git sd --agent": agentUsageText(), "git-sd.1": string(man)}
	for name, text := range texts {
		for _, doc := range docs {
			if strings.Contains(text, doc) {
				t.Errorf("%s refers to %q, which the installed tool's users do not have", name, doc)
			}
		}
	}
	agent := agentUsageText()
	if strings.Contains(agent, "{{") {
		t.Errorf("--agent has an unfilled placeholder")
	}
	for _, v := range notifyVariables {
		for _, name := range []string{"git sd --agent", "git-sd.1"} {
			if !strings.Contains(texts[name], v.Name) {
				t.Errorf("%s does not document %s", name, v.Name)
			}
		}
	}
	for _, k := range notifyEventKinds {
		if !strings.Contains(agent, k.Kind) {
			t.Errorf("--agent does not list event kind %s", k.Kind)
		}
	}
	for _, want := range []string{"notify-send", "osascript", "BurntToast", "$GIT_SD_TITLE"} {
		if !strings.Contains(agent, want) || !strings.Contains(string(man), want) {
			t.Errorf("the desktop notification examples (%s) belong in --agent and the man page", want)
		}
	}
	if !strings.Contains(usageText(), "notify-test") {
		t.Errorf("the usage lists notify-test")
	}
}
