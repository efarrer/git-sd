package main

import (
	"errors"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/metadata"
	"os"
	"os/exec"
	"testing"
)

// moveFixture builds base <- c1 (a.txt line2 -> one) <- c2 (a.txt one -> two)
// <- c3 (independent file), so reordering c2 ahead of c1 conflicts while
// moving c3 anywhere never does.
type moveFixture struct {
	base       string
	c1, c2, c3 git.Commit
	all        []git.Commit
	m          *metadata.Metadata
	origHead   string
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	f := &moveFixture{base: initRepo(t)}
	f.c1 = trackedCommit(t, "a.txt", "line1\none\nline3\n", "c1", "s1111")
	f.c2 = trackedCommit(t, "a.txt", "line1\ntwo\nline3\n", "c2", "s2222")
	f.c3 = trackedCommit(t, "c.txt", "c\n", "c3", "s3333")
	f.origHead = f.c3.Hash
	f.all = []git.Commit{f.c1, f.c2, f.c3}
	f.m = &metadata.Metadata{
		Commits: map[string]metadata.CommitMetadata{
			"s1111": {StackSet: "set"}, "s2222": {StackSet: "set"}, "s3333": {},
		},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	return f
}

// conflict reorders c2 before c1 (a real dependent-commit conflict), attaching
// c2 under c1 as sub-commit "1" if the rebase is later finished.
func (f *moveFixture) conflict(t *testing.T) {
	t.Helper()
	err := rebuildWithIntent(f.m, f.base, f.all, []git.Commit{f.c2, f.c1, f.c3}, "s2222", "s1111", "1", "set")
	var ce rebuildConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want rebuildConflictError", err)
	}
	if !git.RebaseInProgress() {
		t.Fatal("no rebase in progress after conflict")
	}
	if disk, _ := metadata.Load(); disk.PendingMove == nil {
		t.Fatal("pending move not saved on conflict")
	}
}

func TestMoveConflictAbortRestoresOrderAndDropsIntent(t *testing.T) {
	f := newMoveFixture(t)
	f.conflict(t)

	gitT(t, "rebase", "--abort")
	if got := gitT(t, "rev-parse", "HEAD"); got != f.origHead {
		t.Fatalf("HEAD after abort = %s, want %s", got, f.origHead)
	}
	m, _ := metadata.Load()
	settlePendingMove(m, f.base)

	m, _ = metadata.Load()
	if m.PendingMove != nil {
		t.Fatal("pending move not cleared after abort")
	}
	if cm := m.Commits["s2222"]; cm.ParentID != "" || cm.SubRank != "" {
		t.Fatalf("attachment recorded despite abort: %+v", cm)
	}
}

func TestMoveConflictContinueRecordsAttachment(t *testing.T) {
	f := newMoveFixture(t)
	f.conflict(t)

	t.Setenv("GIT_EDITOR", "true")
	for i := 0; i < 5 && git.RebaseInProgress(); i++ {
		// Resolve each conflict with distinct content so no pick ends up empty.
		if err := os.WriteFile("a.txt", []byte("line1\nresolved"+string(rune('0'+i))+"\nline3\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitT(t, "add", "a.txt")
		_ = exec.Command("git", "rebase", "--continue").Run()
	}
	if git.RebaseInProgress() {
		t.Fatal("rebase still in progress after resolving")
	}
	commits, err := git.GetCommitsInRange(f.base)
	if err != nil {
		t.Fatal(err)
	}
	if got := commitIDs(commits); len(got) != 3 || got[0] != "s2222" || got[1] != "s1111" || got[2] != "s3333" {
		t.Fatalf("order after continue = %v, want [s2222 s1111 s3333]", got)
	}

	m, _ := metadata.Load()
	settlePendingMove(m, f.base)
	m, _ = metadata.Load()
	if m.PendingMove != nil {
		t.Fatal("pending move not cleared after continue")
	}
	if cm := m.Commits["s2222"]; cm.ParentID != "s1111" || cm.SubRank != "1" || cm.StackSet != "set" {
		t.Fatalf("attachment not recorded after continue: %+v", cm)
	}
}

func TestMoveWithoutConflictAppliesImmediately(t *testing.T) {
	f := newMoveFixture(t)
	// c3 is independent, so moving it first never conflicts.
	if err := rebuildWithIntent(f.m, f.base, f.all, []git.Commit{f.c3, f.c1, f.c2}, "s3333", "s1111", "2", "set"); err != nil {
		t.Fatal(err)
	}
	if f.m.PendingMove != nil {
		t.Fatal("pending move left behind after a clean rebuild")
	}
	if cm := f.m.Commits["s3333"]; cm.ParentID != "s1111" || cm.SubRank != "2" || cm.StackSet != "set" {
		t.Fatalf("attachment not applied: %+v", cm)
	}
	commits, _ := git.GetCommitsInRange(f.base)
	if got := commitIDs(commits); got[0] != "s3333" || got[1] != "s1111" || got[2] != "s2222" {
		t.Fatalf("order = %v", got)
	}
}

func TestMoveRefusesDirtyTreeAndLeavesRepoUntouched(t *testing.T) {
	f := newMoveFixture(t)
	if err := os.WriteFile("a.txt", []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := rebuildWithIntent(f.m, f.base, f.all, []git.Commit{f.c3, f.c1, f.c2}, "s3333", "s1111", "2", "set")
	var ce rebuildConflictError
	if err == nil || errors.As(err, &ce) {
		t.Fatalf("err = %v, want a plain (non-conflict) failure", err)
	}
	if got := gitT(t, "rev-parse", "HEAD"); got != f.origHead {
		t.Fatalf("HEAD moved to %s", got)
	}
	if f.m.PendingMove != nil {
		t.Fatal("pending move left behind after a failed start")
	}
	if disk, _ := metadata.Load(); disk.PendingMove != nil {
		t.Fatal("pending move left on disk after a failed start")
	}
}

func TestSettleDropsIntentWhenRebaseNeverStartedOrCommitsEdited(t *testing.T) {
	f := newMoveFixture(t)
	for name, pm := range map[string]*metadata.PendingMove{
		"crash before rebase": {OldOrder: commitIDs(f.all), NewOrder: []string{"s3333", "s1111", "s2222"}, MovedID: "s3333", ParentID: "s1111", SubRank: "1"},
		"edited mid-rebase":   {OldOrder: []string{"x"}, NewOrder: []string{"y"}, MovedID: "s3333", ParentID: "s1111", SubRank: "1"},
	} {
		f.m.PendingMove = pm
		settlePendingMove(f.m, f.base)
		if f.m.PendingMove != nil {
			t.Fatalf("%s: pending move not cleared", name)
		}
		if cm := f.m.Commits["s3333"]; cm.ParentID != "" {
			t.Fatalf("%s: attachment recorded: %+v", name, cm)
		}
	}
}
