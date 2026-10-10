package main

import (
	"encoding/json"
	"git-sd/internal/github"
	"git-sd/internal/ui"
	"testing"
)

// TestClassifyPRWithCI pins where CI sits in the precedence: below Merged,
// Closed and Changes requested, above Draft, Review and Approved.
func TestClassifyPRWithCI(t *testing.T) {
	const (
		failing = `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]`
		running = `[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]`
		green   = `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]`
	)
	cases := []struct {
		name     string
		state    string
		decision string
		draft    bool
		rollup   string
		want     ui.MarkerID
	}{
		{"failing CI on an unreviewed PR", "OPEN", "", false, failing, ui.CIFailed},
		{"running CI on an unreviewed PR", "OPEN", "", false, running, ui.CIRunning},
		{"green CI changes nothing", "OPEN", "", false, green, ui.Review},
		{"green CI on an approved PR", "OPEN", "APPROVED", false, green, ui.Approved},
		{"failing CI beats approved", "OPEN", "APPROVED", false, failing, ui.CIFailed},
		{"running CI beats approved", "OPEN", "APPROVED", false, running, ui.CIRunning},
		{"failing CI beats draft", "OPEN", "", true, failing, ui.CIFailed},
		{"running CI beats draft", "OPEN", "", true, running, ui.CIRunning},
		{"failing CI beats running", "OPEN", "", false, running[:len(running)-1] + "," + failing[1:], ui.CIFailed},
		{"changes requested beats failing CI", "OPEN", "CHANGES_REQUESTED", false, failing, ui.Changes},
		{"merged beats failing CI", "MERGED", "", false, failing, ui.Merged},
		{"closed beats running CI", "CLOSED", "", false, running, ui.Closed},
		{"no checks at all", "OPEN", "APPROVED", false, "", ui.Approved},
	}
	for _, c := range cases {
		info := &github.DetailedPRInfo{PRInfo: github.PRInfo{State: c.state}, ReviewDecision: c.decision, IsDraft: c.draft}
		if c.rollup != "" {
			info.StatusCheckRollup = json.RawMessage(c.rollup)
		}
		if got := classifyPR(info); got != c.want {
			t.Errorf("%s: classifyPR = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestClassifyPRWithConflict pins where a GitHub-reported conflict sits: below
// Merged and Closed, above everything else.
func TestClassifyPRWithConflict(t *testing.T) {
	failing := json.RawMessage(`[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]`)
	open := github.PRInfo{State: "OPEN"}
	cases := []struct {
		name string
		info github.DetailedPRInfo
		want ui.MarkerID
	}{
		{"a conflict", github.DetailedPRInfo{PRInfo: open, Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}, ui.Conflict},
		{"conflict by merge state alone", github.DetailedPRInfo{PRInfo: open, MergeStateStatus: "DIRTY"}, ui.Conflict},
		{"conflict beats changes requested", github.DetailedPRInfo{PRInfo: open, ReviewDecision: "CHANGES_REQUESTED", Mergeable: "CONFLICTING"}, ui.Conflict},
		{"conflict beats failing CI", github.DetailedPRInfo{PRInfo: open, StatusCheckRollup: failing, Mergeable: "CONFLICTING"}, ui.Conflict},
		{"conflict beats draft", github.DetailedPRInfo{PRInfo: open, IsDraft: true, Mergeable: "CONFLICTING"}, ui.Conflict},
		{"conflict beats approved", github.DetailedPRInfo{PRInfo: open, ReviewDecision: "APPROVED", Mergeable: "CONFLICTING"}, ui.Conflict},
		{"merged beats conflict", github.DetailedPRInfo{PRInfo: github.PRInfo{State: "MERGED"}, Mergeable: "CONFLICTING"}, ui.Merged},
		{"closed beats conflict", github.DetailedPRInfo{PRInfo: github.PRInfo{State: "CLOSED"}, Mergeable: "CONFLICTING"}, ui.Closed},
		{"GitHub still computing: no conflict", github.DetailedPRInfo{PRInfo: open, Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN"}, ui.Review},
		{"blocked is not a conflict", github.DetailedPRInfo{PRInfo: open, ReviewDecision: "APPROVED", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED"}, ui.Approved},
	}
	for _, c := range cases {
		info := c.info
		if got := classifyPR(&info); got != c.want {
			t.Errorf("%s: classifyPR = %q, want %q", c.name, got, c.want)
		}
	}
}
