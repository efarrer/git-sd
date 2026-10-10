package main

import (
	"git-sd/internal/git"
	"git-sd/internal/metadata"
	"git-sd/internal/ui"
	"slices"
	"strings"
	"testing"
)

func TestStatusColumns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"default", defaultStatusColumns, []string{"Icon", "Status", "Index", "Stack Set", "git-sd-id", "Hash", "Message", "PR"}},
		{"a subset keeps the order given", "Index,Message", []string{"Index", "Message"}},
		{"reordered", "PR,Hash,Index", []string{"PR", "Hash", "Index"}},
		{"case and surrounding spaces don't matter", " status , INDEX ,pr", []string{"Status", "Index", "PR"}},
		{"spaces and hyphens in names are optional", "stackset,gitsdid,Git-SD-ID", []string{"Stack Set", "git-sd-id", "git-sd-id"}},
		{"empty entries are skipped", "Index,,Hash,", []string{"Index", "Hash"}},
		{"the word without the icon", "Status,Message", []string{"Status", "Message"}},
	}
	for _, c := range cases {
		got, err := statusColumns(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: statusColumns(%q) = %v, %v; want %v", c.name, c.in, got, err, c.want)
		}
	}
}

func TestStatusColumnsRejectsUnknownAndEmpty(t *testing.T) {
	_, err := statusColumns("Index,Colour,Hash")
	if err == nil || !strings.Contains(err.Error(), "unknown status column 'Colour'") || !strings.Contains(err.Error(), "valid: Icon, Status, Index, Stack Set, git-sd-id, Hash, Message, PR") {
		t.Errorf("unknown column: got %v", err)
	}
	for _, in := range []string{"", " , ,"} {
		if _, err := statusColumns(in); err == nil || !strings.Contains(err.Error(), "no columns") {
			t.Errorf("statusColumns(%q) should be an error naming the problem, got %v", in, err)
		}
	}
}

func TestStatusHeaderAndRow(t *testing.T) {
	cols := []string{"Icon", "Status", "PR"}
	if got := statusHeader(cols); !slices.Equal(got, []string{"", "Status", "PR"}) {
		t.Errorf("the Icon column's header must be blank, got %q", got)
	}
	cells := map[string]string{"Icon": "\U0001F4E6", "Status": "Assigned", "Index": "3", "PR": "https://x/pull/1"}
	if got := statusRow(cols, cells); !slices.Equal(got, []string{"\U0001F4E6", "Assigned", "https://x/pull/1"}) {
		t.Errorf("statusRow = %q", got)
	}
	if got := statusRow([]string{"Stack Set", "Hash"}, cells); !slices.Equal(got, []string{"", ""}) {
		t.Errorf("a column with no cell is blank, got %q", got)
	}
}

func TestSubCommitMarker(t *testing.T) {
	parent := metadata.CommitMetadata{StackSet: "s", PRNumber: 7}
	synced := metadata.CommitMetadata{StackSet: "s", ParentID: "p", SyncedFingerprint: "fp1", SyncedPR: 7}
	cases := []struct {
		name         string
		sub          metadata.CommitMetadata
		parent       metadata.CommitMetadata
		parentMarker ui.MarkerID
		fingerprint  string
		want         bool
	}{
		{"in the PR as of the last sync", synced, parent, ui.Review, "fp1", false},
		{"never recorded (just attached)", metadata.CommitMetadata{ParentID: "p"}, parent, ui.Review, "fp1", true},
		{"amended or reworded since the sync", synced, parent, ui.Review, "fp2", true},
		{"moved to a different PR", metadata.CommitMetadata{ParentID: "p", SyncedFingerprint: "fp1", SyncedPR: 3}, parent, ui.Review, "fp1", true},
		{"parent has no PR yet: nothing to be out of", metadata.CommitMetadata{ParentID: "p"}, metadata.CommitMetadata{StackSet: "s"}, ui.Assigned, "fp1", false},
		{"parent's PR is merged", metadata.CommitMetadata{ParentID: "p"}, parent, ui.Merged, "fp1", false},
		{"parent's PR is closed", metadata.CommitMetadata{ParentID: "p"}, parent, ui.Closed, "fp1", false},
		{"parent's state unknown still counts", metadata.CommitMetadata{ParentID: "p"}, parent, ui.Unavailable, "fp1", true},
		{"approved parent, sub-commit added after", metadata.CommitMetadata{ParentID: "p"}, parent, ui.Approved, "fp1", true},
	}
	for _, c := range cases {
		id, got := subCommitMarker(c.sub, c.parent, c.parentMarker, c.fingerprint)
		if got != c.want || (got && id != ui.Unsynced) {
			t.Errorf("%s: subCommitMarker = %q, %v; want unsynced=%v", c.name, id, got, c.want)
		}
	}
}

func TestCommitFingerprint(t *testing.T) {
	msg := "Fix the thing\n\ngit-sd-id: s1\n"
	base := commitFingerprint("patch1", msg)
	if base != commitFingerprint("patch1", msg) {
		t.Error("the fingerprint must be stable")
	}
	if base == commitFingerprint("patch2", msg) {
		t.Error("a different diff must change the fingerprint")
	}
	if base == commitFingerprint("patch1", "Fix the other thing\n\ngit-sd-id: s1\n") {
		t.Error("a reworded commit must change the fingerprint")
	}
	// The git-sd-id trailer is not part of what is pushed, so it doesn't count.
	if base != commitFingerprint("patch1", "Fix the thing\n\ngit-sd-id: s999\n") {
		t.Error("the git-sd-id trailer must not affect the fingerprint")
	}
	// Parts can't run into each other.
	if commitFingerprint("ab", "c\n") == commitFingerprint("a", "bc\n") {
		t.Error("fingerprint parts must be length-prefixed")
	}
}

func TestRecordSyncedRecordsEveryCommitOfTheGroup(t *testing.T) {
	m := &metadata.Metadata{Commits: map[string]metadata.CommitMetadata{
		"top": {StackSet: "s"},
		"sub": {StackSet: "s", ParentID: "top"},
	}}
	g := PRGroup{TopLevelID: "top", Commits: []git.Commit{
		{Hash: "h1", Message: "Top\n\ngit-sd-id: top\n"},
		{Hash: "h2", Message: "Fix\n\ngit-sd-id: sub\n"},
	}}
	recordSynced(m, g, 9, map[string]string{"h1": "p1", "h2": "p2"})
	for id, wantPatch := range map[string]string{"top": "p1", "sub": "p2"} {
		cm := m.Commits[id]
		msg := g.Commits[0].Message
		if id == "sub" {
			msg = g.Commits[1].Message
		}
		if cm.SyncedPR != 9 || cm.SyncedFingerprint != commitFingerprint(wantPatch, msg) {
			t.Errorf("%s not recorded correctly: %+v", id, cm)
		}
	}
	// Without patch-ids nothing is recorded (and nothing is cleared).
	before := m.Commits["sub"]
	recordSynced(m, g, 10, nil)
	if m.Commits["sub"] != before {
		t.Error("recordSynced with no patch-ids must change nothing")
	}
}

func TestStatusWordIsColoredOnlyWhenColorIsOn(t *testing.T) {
	approved := ui.MarkerFor(ui.Approved)
	if got := statusWord(approved, false); got != "Approved" {
		t.Errorf("without color the word is plain, got %q", got)
	}
	if got := statusWord(approved, true); got != "\x1b[32mApproved\x1b[0m" {
		t.Errorf("an approved PR's word is green, got %q", got)
	}
	// A marker with no color stays plain even when color is on.
	if got := statusWord(ui.MarkerFor(ui.Assigned), true); got != "Assigned" {
		t.Errorf("Assigned has no color, got %q", got)
	}
}
