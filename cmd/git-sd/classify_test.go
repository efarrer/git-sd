package main

import (
	"encoding/json"
	"github.com/efarrer/git-sd/internal/github"
	"github.com/efarrer/git-sd/internal/metadata"
	"github.com/efarrer/git-sd/internal/ui"
	"slices"
	"testing"
)

func TestClassifyPR(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		decision string
		draft    bool
		want     ui.MarkerID
	}{
		{"merged wins over everything", "MERGED", "CHANGES_REQUESTED", true, ui.Merged},
		{"closed wins over everything", "CLOSED", "APPROVED", true, ui.Closed},
		{"changes requested", "OPEN", "CHANGES_REQUESTED", false, ui.Changes},
		{"changes requested beats draft", "OPEN", "CHANGES_REQUESTED", true, ui.Changes},
		{"draft beats approved", "OPEN", "APPROVED", true, ui.Draft},
		{"draft with no decision", "OPEN", "", true, ui.Draft},
		{"draft awaiting review", "OPEN", "REVIEW_REQUIRED", true, ui.Draft},
		{"approved", "OPEN", "APPROVED", false, ui.Approved},
		{"review required", "OPEN", "REVIEW_REQUIRED", false, ui.Review},
		{"an unreviewed open PR (no decision) is Review, not Assigned", "OPEN", "", false, ui.Review},
		{"an unknown decision falls back to Review", "OPEN", "SOMETHING_NEW", false, ui.Review},
	}
	for _, c := range cases {
		got := classifyPR(&github.DetailedPRInfo{PRInfo: github.PRInfo{State: c.state}, ReviewDecision: c.decision, IsDraft: c.draft})
		if got != c.want {
			t.Errorf("%s: classifyPR(state=%s decision=%q draft=%v) = %q, want %q", c.name, c.state, c.decision, c.draft, got, c.want)
		}
	}
}

func TestMarkerIDForUntrackedReadyAndAssigned(t *testing.T) {
	lookups := map[int]prLookup{7: {info: &github.DetailedPRInfo{ReviewDecision: "APPROVED"}}}
	cases := []struct {
		name    string
		tracked bool
		cm      metadata.CommitMetadata
		want    ui.MarkerID
	}{
		{"no git-sd-id", false, metadata.CommitMetadata{}, ui.Untracked},
		{"tracked, no Stack Set", true, metadata.CommitMetadata{}, ui.Ready},
		{"assigned, no PR", true, metadata.CommitMetadata{StackSet: "s"}, ui.Assigned},
		{"assigned with a PR", true, metadata.CommitMetadata{StackSet: "s", PRNumber: 7}, ui.Approved},
	}
	for _, c := range cases {
		if got := markerID(c.tracked, c.cm, lookups); got != c.want {
			t.Errorf("%s: markerID = %q, want %q", c.name, got, c.want)
		}
	}
}

// knownUnproduced lists the markers in the legend that no code produces yet.
// This is the guard against the legend promising states that `status` never
// shows (bug B-020): the list must shrink to nothing as those states are
// implemented, and the test fails if a marker is in neither set.
var knownUnproduced = []ui.MarkerID{}

func TestEveryLegendMarkerIsProducedOrKnownToBeMissing(t *testing.T) {
	produced := map[ui.MarkerID]bool{}
	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		for _, decision := range []string{"", "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"} {
			for _, draft := range []bool{false, true} {
				for _, rollup := range []string{"", `[{"__typename":"StatusContext","state":"FAILURE"}]`, `[{"__typename":"StatusContext","state":"PENDING"}]`} {
					for _, mergeable := range []string{"", "CONFLICTING"} {
						produced[classifyPR(&github.DetailedPRInfo{PRInfo: github.PRInfo{State: state}, ReviewDecision: decision, IsDraft: draft, StatusCheckRollup: json.RawMessage(rollup), Mergeable: mergeable})] = true
					}
				}
			}
		}
	}
	for _, tracked := range []bool{false, true} {
		for _, set := range []string{"", "s"} {
			for _, pr := range []int{0, 7, 8} {
				lookups := map[int]prLookup{7: {info: &github.DetailedPRInfo{}}, 8: {}}
				produced[markerID(tracked, metadata.CommitMetadata{StackSet: set, PRNumber: pr}, lookups)] = true
			}
		}
	}
	produced[withOperation(ui.Review, true)] = true // Syncing: a sync or merge is running
	if id, ok := subCommitMarker(metadata.CommitMetadata{}, metadata.CommitMetadata{PRNumber: 1}, ui.Review, ""); ok {
		produced[id] = true
	}
	for _, m := range ui.Markers() {
		missing := slices.Contains(knownUnproduced, m.ID)
		switch {
		case !produced[m.ID] && !missing:
			t.Errorf("marker %s (%s) is in the legend but no code produces it; implement it or add it to knownUnproduced", m.ID, m.Word)
		case produced[m.ID] && missing:
			t.Errorf("marker %s is produced now: remove it from knownUnproduced", m.ID)
		}
	}
}
