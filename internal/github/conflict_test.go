package github

import "testing"

func TestHasConflict(t *testing.T) {
	cases := []struct {
		mergeable, state string
		want             bool
	}{
		{"CONFLICTING", "DIRTY", true},
		{"CONFLICTING", "", true},
		{"", "DIRTY", true},
		{"CONFLICTING", "UNKNOWN", true},
		{"MERGEABLE", "CLEAN", false},
		{"MERGEABLE", "BLOCKED", false}, // blocked by branch protection, not a conflict
		{"MERGEABLE", "BEHIND", false},
		{"UNKNOWN", "UNKNOWN", false}, // GitHub is still computing: say nothing
		{"", "", false},
	}
	for _, c := range cases {
		got := (&DetailedPRInfo{Mergeable: c.mergeable, MergeStateStatus: c.state}).HasConflict()
		if got != c.want {
			t.Errorf("mergeable=%q mergeStateStatus=%q: HasConflict = %v, want %v", c.mergeable, c.state, got, c.want)
		}
	}
}
