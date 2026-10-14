package ui

import "fmt"

// MarkerID identifies one status marker: what `git sd status` shows for a
// commit (an icon and a word) and what `git sd status --legend` explains.
type MarkerID string

const (
	Untracked   MarkerID = "untracked"
	Ready       MarkerID = "ready"
	Assigned    MarkerID = "assigned"
	Unsynced    MarkerID = "unsynced"
	Unavailable MarkerID = "unavailable"
	Syncing     MarkerID = "syncing"
	Conflict    MarkerID = "conflict"
	Changes     MarkerID = "changes"
	CIFailed    MarkerID = "ci-failed"
	CIRunning   MarkerID = "ci-running"
	Draft       MarkerID = "draft"
	Review      MarkerID = "review"
	Approved    MarkerID = "approved"
	Merged      MarkerID = "merged"
	Closed      MarkerID = "closed"
)

// Color is a marker's color, applied to its word on a terminal.
type Color int

const (
	ColorNone Color = iota
	ColorDim
	ColorRed
	ColorGreen
	ColorYellow
)

const (
	groupCommit = "Commit States"
	groupPR     = "PR States"
)

// Marker is the single source of truth for one status state: the icon and word
// shown in the status table, the color, and the legend entry. The legend is
// printed from this table, so a state can't be documented without being defined
// here, and the tests flag any state that no code produces.
type Marker struct {
	ID     MarkerID
	Icon   string // empty for none (a Ready commit shows only its word)
	Word   string
	Color  Color
	Group  string // legend heading
	Text   string // legend description
	Action string // optional legend hint
}

// markers is in legend order (and, within the PR states, priority order).
var markers = []Marker{
	{Untracked, "—", "Untracked", ColorDim, groupCommit, "Commit has no git-sd-id yet; assign, move or fixup tags it.", ""},                                                                                                                                                          // untracked
	{Ready, "", "Ready", ColorDim, groupCommit, "Commit has a git-sd-id and is ready to be assigned to a Stack Set.", "Run 'git sd assign <set-name>:<index-list>' to group it."},                                                                                                    // ready
	{Assigned, "📦", "Assigned", ColorNone, groupCommit, "Commit is assigned to a Stack Set and has no PR yet.", "Run 'git sd sync' to create/update the PR."},                                                                                                                        // assigned
	{Unsynced, "📤", "Unsynced", ColorYellow, groupCommit, "A sub-commit that is not in its PR yet: added, changed or moved into the PR since the last sync.", "Run 'git sd sync' to put it in the PR."},                                                                              // unsynced
	{Unavailable, "❓", "Unknown", ColorYellow, groupPR, "The PR's state could not be read (the lookup failed); the PR itself may be fine.", "Re-run 'git sd status'."},                                                                                                               // unavailable
	{Syncing, "🔄", "Syncing", ColorYellow, groupPR, "A 'git sd sync' or 'git sd merge' is running on this Stack Set right now (seen from another terminal).", ""},                                                                                                                    // syncing
	{Conflict, "💥", "Conflict", ColorRed, groupPR, "GitHub reports a merge conflict between the PR and its base branch; it cannot be merged as it is.", "Rebase onto the updated base and resolve the conflicts ('git sd rebase', or a standard 'git rebase'), then 'git sd sync'."}, // conflict
	{Changes, "❌", "Changes", ColorRed, groupPR, "Address the reviewer's feedback.", "Create a fix commit, then 'git sd fixup <target-index>' to attach it as the next sub-commit of that PR (or 'git sd move' for manual placement/reordering)."},                                   // changes
	{CIFailed, "🚨", "CI failed", ColorRed, groupPR, "Code or tests failed.", "Fix the code and sync."},                                                                                                                                                                               // ci-failed
	{CIRunning, "🟡", "CI running", ColorYellow, groupPR, "Wait for results.", ""},                                                                                                                                                                                                    // ci-running
	{Draft, "🚧", "Draft", ColorYellow, groupPR, "The PR is a work-in-progress; it can't be merged yet.", ""},                                                                                                                                                                         // draft
	{Review, "👀", "Review", ColorYellow, groupPR, "Waiting for a review (none yet, or more required).", ""},                                                                                                                                                                          // review
	{Approved, "✅", "Approved", ColorGreen, groupPR, "Ready to merge.", ""},                                                                                                                                                                                                          // approved
	{Merged, "🚢", "Merged", ColorDim, groupPR, "Final state; the PR landed.", ""},                                                                                                                                                                                                    // merged
	{Closed, "🌑", "Closed", ColorDim, groupPR, "The PR was closed without merging.", ""},                                                                                                                                                                                             // closed
}

// Markers returns every marker in legend order.
func Markers() []Marker {
	out := make([]Marker, len(markers))
	copy(out, markers)
	return out
}

// MarkerFor returns the marker with the given id.
func MarkerFor(id MarkerID) Marker {
	for _, m := range markers {
		if m.ID == id {
			return m
		}
	}
	panic(fmt.Sprintf("ui: unknown marker %q", id))
}
