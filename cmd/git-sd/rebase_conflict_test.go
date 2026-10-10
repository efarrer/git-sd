package main

import (
	"git-sd/internal/git"
	"git-sd/internal/metadata"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// rebaseConflictFixture builds a work branch of four tracked commits on top of
// base — saaaa (landed upstream, dropped), sbbbb (clean), scccc (conflicts with
// upstream), sdddd (clean) — plus an "upstream" branch that landed saaaa's
// change and also edited the line scccc edits. It then runs rebaseOnto, which
// must stop on the conflict at scccc with a standard git rebase in progress.
type rebaseConflictFixture struct {
	origHead string
	m        *metadata.Metadata
	err      error
}

func newRebaseConflictFixture(t *testing.T) *rebaseConflictFixture {
	t.Helper()
	base := initRepo(t)
	gitT(t, "checkout", "-q", "-b", "upstream", base)
	commitFile(t, "landed.txt", "landed\n", "landed upstream")
	commitFile(t, "a.txt", "line1\nUPSTREAM\nline3\n", "upstream edit")

	gitT(t, "checkout", "-q", "-b", "work", base)
	landed := trackedCommit(t, "landed.txt", "landed\n", "landed", "saaaa")
	b := trackedCommit(t, "b.txt", "b\n", "clean b", "sbbbb")
	c := trackedCommit(t, "a.txt", "line1\nLOCAL\nline3\n", "local edit", "scccc")
	d := trackedCommit(t, "d.txt", "d\n", "clean d", "sdddd")

	f := &rebaseConflictFixture{origHead: d.Hash}
	f.m = &metadata.Metadata{
		Commits: map[string]metadata.CommitMetadata{
			"saaaa": {StackSet: "set", PRNumber: 1, BranchName: "landed-saaaa"},
			"sbbbb": {StackSet: "set"},
			"scccc": {StackSet: "set"},
			"sdddd": {StackSet: "set"},
		},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	snap, err := takeSnapshot(f.m)
	if err != nil {
		t.Fatal(err)
	}
	_, f.err = rebaseOnto(f.m, snap, "upstream", []git.Commit{landed, b, c, d}, map[string]bool{landed.Hash: true}, nil, nil)
	return f
}

func (f *rebaseConflictFixture) requireStopped(t *testing.T) {
	t.Helper()
	if f.err == nil {
		t.Fatal("rebaseOnto succeeded, want a conflict")
	}
	if !git.RebaseInProgress() {
		t.Fatalf("no git rebase in progress after a conflict: %v", f.err)
	}
	for _, want := range []string{"Conflict while rebasing onto upstream", "local edit", "a.txt", "git rebase --continue", "git rebase --skip", "git rebase --abort", "theirs"} {
		if !strings.Contains(f.err.Error(), want) {
			t.Fatalf("conflict message missing %q:\n%v", want, f.err)
		}
	}
	disk, err := metadata.Load()
	if err != nil {
		t.Fatal(err)
	}
	if disk.PendingRebase == nil || len(disk.RebaseBackups) != 1 {
		t.Fatalf("expected a saved pending rebase and one backup, got %+v", disk)
	}
}

// resolveFile writes content to name, stages it and runs git rebase --continue.
func resolveFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, "add", name)
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rebase --continue failed: %v\n%s", err, out)
	}
}

func resolveAndContinue(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile("a.txt", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, "add", "a.txt")
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rebase --continue failed: %v\n%s", err, out)
	}
}

func settle(t *testing.T) *metadata.Metadata {
	t.Helper()
	m, err := metadata.Load()
	if err != nil {
		t.Fatal(err)
	}
	if m.PendingRebase == nil {
		t.Fatal("no pending rebase to settle")
	}
	settlePendingRebase(m)
	after, err := metadata.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.PendingRebase != nil {
		t.Fatal("pending rebase not cleared after settling")
	}
	return after
}

func TestRebaseConflictAbortRestoresBranchAndDiscardsBackup(t *testing.T) {
	f := newRebaseConflictFixture(t)
	f.requireStopped(t)

	gitT(t, "rebase", "--abort")
	m := settle(t)

	if got := gitT(t, "rev-parse", "HEAD"); got != f.origHead {
		t.Fatalf("HEAD after abort = %s, want %s", got, f.origHead)
	}
	if len(m.RebaseBackups) != 0 {
		t.Fatalf("backup left behind after abort: %+v", m.RebaseBackups)
	}
	for _, id := range []string{"saaaa", "sbbbb", "scccc", "sdddd"} {
		if _, ok := m.Commits[id]; !ok {
			t.Fatalf("abort retired %s; metadata must be unchanged", id)
		}
	}
	if len(m.PendingBranchDeletions) != 0 {
		t.Fatalf("abort queued branch deletions: %+v", m.PendingBranchDeletions)
	}
}

func TestRebaseConflictContinueRetiresLandedAndKeepsBackup(t *testing.T) {
	f := newRebaseConflictFixture(t)
	f.requireStopped(t)

	resolveAndContinue(t, "line1\nRESOLVED\nline3\n")
	if git.RebaseInProgress() {
		t.Fatal("rebase still in progress after --continue")
	}
	commits, err := git.GetCommitsInRange("upstream")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(commitIDs(commits), ","); got != "sbbbb,scccc,sdddd" {
		t.Fatalf("branch after continue = %s, want sbbbb,scccc,sdddd (the landed commit dropped, the rest replayed)", got)
	}

	m := settle(t)
	if _, ok := m.Commits["saaaa"]; ok {
		t.Fatal("landed commit saaaa should be retired after --continue")
	}
	for _, id := range []string{"sbbbb", "scccc", "sdddd"} {
		if _, ok := m.Commits[id]; !ok {
			t.Fatalf("surviving commit %s was retired", id)
		}
	}
	if len(m.PendingBranchDeletions) != 1 || m.PendingBranchDeletions[0].Branch != "landed-saaaa" {
		t.Fatalf("expected landed-saaaa's branch queued for deletion, got %+v", m.PendingBranchDeletions)
	}
	if len(m.RebaseBackups) != 1 {
		t.Fatalf("backup should be kept after --continue, got %d", len(m.RebaseBackups))
	}

	// The kept backup still works: --rollback restores branch and metadata.
	if err := restoreBackup(m, 0); err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, "rev-parse", "HEAD"); got != f.origHead {
		t.Fatalf("HEAD after rollback = %s, want %s", got, f.origHead)
	}
	if _, ok := m.Commits["saaaa"]; !ok {
		t.Fatal("rollback did not restore the retired metadata")
	}
}

func TestRebaseConflictSkipTreatsCommitAsLanded(t *testing.T) {
	f := newRebaseConflictFixture(t)
	f.requireStopped(t)

	gitT(t, "rebase", "--skip")
	if git.RebaseInProgress() {
		t.Fatal("rebase still in progress after --skip")
	}
	m := settle(t)
	if _, ok := m.Commits["scccc"]; ok {
		t.Fatal("skipped commit scccc should be retired as landed")
	}
	for _, id := range []string{"sbbbb", "sdddd"} {
		if _, ok := m.Commits[id]; !ok {
			t.Fatalf("surviving commit %s was retired", id)
		}
	}
}

func TestRebaseOntoDroppingEverythingResetsToTarget(t *testing.T) {
	base := initRepo(t)
	gitT(t, "checkout", "-q", "-b", "upstream", base)
	commitFile(t, "landed.txt", "landed\n", "landed upstream")
	target := gitT(t, "rev-parse", "HEAD")

	gitT(t, "checkout", "-q", "-b", "work", base)
	landed := trackedCommit(t, "landed.txt", "landed\n", "landed", "saaaa")
	m := &metadata.Metadata{
		Commits:   map[string]metadata.CommitMetadata{"saaaa": {StackSet: "set", BranchName: "landed-saaaa"}},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	snap, err := takeSnapshot(m)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := rebaseOnto(m, snap, "upstream", []git.Commit{landed}, map[string]bool{landed.Hash: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, "rev-parse", "HEAD"); got != target {
		t.Fatalf("HEAD = %s, want the target %s", got, target)
	}
	if retired != 1 || m.PendingRebase != nil {
		t.Fatalf("retired=%d pending=%+v, want 1 retired and no pending rebase", retired, m.PendingRebase)
	}
}

func TestStaleRebaseIntentIsDiscardedWhenNothingChanged(t *testing.T) {
	base := initRepo(t)
	gitT(t, "checkout", "-q", "-b", "work", base)
	c := trackedCommit(t, "b.txt", "b\n", "b", "sbbbb")
	m := &metadata.Metadata{
		Commits:   map[string]metadata.CommitMetadata{"sbbbb": {StackSet: "set"}},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	snap, err := takeSnapshot(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateRef(snap.Ref, snap.SHA); err != nil {
		t.Fatal(err)
	}
	m.RebaseBackups = append(m.RebaseBackups, snap)
	// A crash after saving the intent but before the rebase started.
	m.PendingRebase = &metadata.PendingRebase{OrigHead: c.Hash, TargetTip: base, BackupRef: snap.Ref}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	after := settle(t)
	if len(after.RebaseBackups) != 0 {
		t.Fatalf("stale intent left its backup behind: %+v", after.RebaseBackups)
	}
	if _, ok := after.Commits["sbbbb"]; !ok {
		t.Fatal("stale intent retired a commit")
	}
}

func TestVanishedCommitWithUnverifiablePRIsNotRetired(t *testing.T) {
	// A tracked commit with a PR that is no longer on the branch and whose PR
	// state can't be confirmed (no GitHub here) must be left alone, never
	// retired: retiring would queue its branch for deletion.
	initRepo(t)
	m := &metadata.Metadata{
		Commits:   map[string]metadata.CommitMetadata{"sgone": {StackSet: "set", PRNumber: 424242, BranchName: "gone-sgone"}},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	retired, err := finishRebase(m, "HEAD", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if retired != 0 {
		t.Fatalf("retired %d, want 0 (PR state unverifiable)", retired)
	}
	if _, ok := m.Commits["sgone"]; !ok || len(m.PendingBranchDeletions) != 0 {
		t.Fatalf("commit retired or branch queued despite an unverifiable PR: %+v", m)
	}
}
