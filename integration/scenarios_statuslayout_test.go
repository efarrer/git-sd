package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rowWith returns the status row whose text contains msg.
func rowWith(t *testing.T, out, msg string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, msg) {
			return line
		}
	}
	t.Fatalf("no row containing %q in:\n%s", msg, out)
	return ""
}

// TestSDStatusShowsStateWordsAndHonoursColumns covers the status layout without
// GitHub: the Icon and Status columns, the Ready word, a sub-commit showing its
// parent's state, and sd.status.columns (subset, order, unknown name).
func TestSDStatusShowsStateWordsAndHonoursColumns(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	status := func(args ...string) string {
		t.Helper()
		out, err := e.sd(append([]string{"status"}, args...)...)
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		return out
	}
	statusOf := func(out, msg string) string { return statusCell(t, out, rowWith(t, out, msg), "Status") }
	iconOf := func(out, msg string) string { return statusCell(t, out, rowWith(t, out, msg), "Icon") }

	// Untracked: the em dash and the word.
	out := status()
	if got := statusOf(out, "Commit A"); got != "Untracked" {
		t.Errorf("an untracked commit's Status = %q, want Untracked:\n%s", got, out)
	}
	if got := iconOf(out, "Commit A"); got != "—" {
		t.Errorf("an untracked commit's Icon = %q, want the em dash", got)
	}
	// The header: a blank Icon column, then the words.
	header := rowWith(t, out, "Message")
	if !strings.HasPrefix(header, "    Status") && !strings.HasPrefix(strings.TrimLeft(header, " "), "Status") {
		t.Errorf("the Icon column's header should be blank, got %q", header)
	}

	// Tracked but not assigned: Ready, with no icon.
	if o, err := e.sd("track"); err != nil {
		t.Fatalf("track: %v\n%s", err, o)
	}
	out = status()
	if got, icon := statusOf(out, "Commit A"), iconOf(out, "Commit A"); got != "Ready" || icon != "" {
		t.Errorf("a tracked, unassigned commit should be Ready with no icon, got %q / %q:\n%s", got, icon, out)
	}

	// Assigned, no PR yet.
	if o, err := e.sd("assign", "tmp:0-1"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	out = status()
	if got, icon := statusOf(out, "Commit A"), iconOf(out, "Commit A"); got != "Assigned" || icon != "📦" {
		t.Errorf("an assigned commit should be 📦 Assigned, got %q / %q", icon, got)
	}
	if got := statusOf(out, "Commit C"); got != "Ready" {
		t.Errorf("the unassigned commit should still be Ready, got %q", got)
	}

	// With a PR (the fake gh reports it open and unreviewed) the parent shows
	// Review. A sub-commit repeats none of that: it is blank while it is in the
	// PR, and Unsynced while it is not.
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	e.commitFile("Fix_A.txt", "Fix for A")
	if o, err := e.sd("track"); err != nil {
		t.Fatalf("track: %v\n%s", err, o)
	}
	if o, err := e.sd("fixup", "0"); err != nil {
		t.Fatalf("fixup: %v\n%s", err, o)
	}
	subStatus := func(out string) (icon, word string) {
		sub := rowWith(t, out, "Fix for A")
		return statusCell(t, out, sub, "Icon"), statusCell(t, out, sub, "Status")
	}
	out = status()
	if got := statusCell(t, out, rowWith(t, out, "Fix for A"), "Index"); !strings.HasPrefix(got, "0.") {
		t.Fatalf("the fix should be a sub-commit of 0, got index %q:\n%s", got, out)
	}
	if got := statusOf(out, "Commit A"); got != "Review" {
		t.Errorf("the parent should show Review, got %q", got)
	}
	if msg := statusCell(t, out, rowWith(t, out, "Fix for A"), "Message"); !strings.HasPrefix(msg, "└─ ") {
		t.Errorf("a sub-commit's message should be indented, got %q", msg)
	}
	if icon, word := subStatus(out); icon != "📤" || word != "Unsynced" {
		t.Errorf("a sub-commit not yet synced shows 📤 Unsynced, got %q / %q:\n%s", icon, word, out)
	}

	// Synced: nothing to say, and certainly not the parent's state.
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	out = status()
	if icon, word := subStatus(out); icon != "" || word != "" {
		t.Errorf("a synced sub-commit has a blank Icon/Status, got %q / %q:\n%s", icon, word, out)
	}
	if got := statusOf(out, "Commit A"); got != "Review" {
		t.Errorf("the parent still shows Review, got %q", got)
	}

	// Amended after the sync: not in the PR as it is now.
	// (The fix is no longer HEAD after fixup moved it next to its parent, so
	// stop on it with an interactive rebase, change it, and carry on.)
	gitWith := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = e.repo
		cmd.Env = append(os.Environ(), `GIT_SEQUENCE_EDITOR=sed -i 's/^pick \(.*Fix for A\)/edit \1/'`, "GIT_EDITOR=true")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitWith("rebase", "-q", "-i", "origin/main")
	if err := os.WriteFile(filepath.Join(e.repo, "Fix_A.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.git(e.repo, "commit", "-q", "-a", "--amend", "--no-edit")
	gitWith("rebase", "--continue")
	out = status()
	if _, word := subStatus(out); word != "Unsynced" {
		t.Errorf("an amended sub-commit is Unsynced again, got %q:\n%s", word, out)
	}
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	out = status()
	if icon, word := subStatus(out); icon != "" || word != "" {
		t.Errorf("after syncing the amend it is blank again, got %q / %q", icon, word)
	}

	// Columns: a subset, in the order given, case-insensitively.
	if o, err := e.sd("status"); err != nil {
		t.Fatal(o)
	}
	e.git(e.repo, "config", "sd.status.columns", "message, INDEX ,status")
	out = status()
	hdr := strings.Fields(rowWith(t, out, "Message"))
	if strings.Join(hdr, " ") != "Message Index Status" {
		t.Errorf("columns should be Message, Index, Status in that order, got %q:\n%s", hdr, out)
	}
	if strings.Contains(out, "📦") || strings.Contains(out, "Hash") || strings.Contains(out, "git-sd-id") {
		t.Errorf("columns that were not selected must not appear:\n%s", out)
	}

	// An unknown column is an error that names the valid ones, and prints no table.
	e.git(e.repo, "config", "sd.status.columns", "Index,Colour")
	out, err := e.sd("status")
	if err == nil {
		t.Fatalf("an unknown column should fail:\n%s", out)
	}
	if !strings.Contains(out, "Error: unknown status column 'Colour' (valid: Icon, Status, Index, Stack Set, git-sd-id, Hash, Message, PR)") {
		t.Errorf("the error should name the column and the valid ones:\n%s", out)
	}
	if strings.Contains(out, "Commit A") {
		t.Errorf("no table should be printed on a bad column:\n%s", out)
	}
}
