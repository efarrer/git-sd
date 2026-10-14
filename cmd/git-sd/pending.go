package main

import (
	"errors"
	"fmt"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/metadata"
	"os"
	"slices"
)

const pendingInProgressMsg = "a git-sd move, fixup or rebase stopped on a conflict — run 'git rebase --continue' to finish it or 'git rebase --abort' to undo it"

func commitIDs(commits []git.Commit) []string {
	ids := make([]string, 0, len(commits))
	for _, c := range commits {
		ids = append(ids, git.ExtractTrailer(c.Message, "git-sd-id"))
	}
	return ids
}

// rebuildWithIntent rebuilds the branch into newCommits' order and records the
// attachment change for movedID. The change is saved as a PendingMove before the
// rebase starts, so a conflict can be finished or aborted with plain git and
// reconciled later (see reconcilePendingMove). On success the change is applied
// to m and the intent cleared; the caller saves m.
func rebuildWithIntent(m *metadata.Metadata, since string, oldCommits, newCommits []git.Commit, movedID, parentID, subRank, stackSet string) error {
	pm := &metadata.PendingMove{
		OldOrder: commitIDs(oldCommits),
		NewOrder: commitIDs(newCommits),
		MovedID:  movedID,
		ParentID: parentID,
		SubRank:  subRank,
		StackSet: stackSet,
	}
	m.PendingMove = pm
	if err := m.Save(); err != nil {
		m.PendingMove = nil
		return fmt.Errorf("saving pending move: %w", err)
	}
	if err := git.RebuildCommitChainRebase(since, newCommits); err != nil {
		if git.RebaseInProgress() {
			return rebuildConflictError{err}
		}
		m.PendingMove = nil
		_ = m.Save()
		return err
	}
	pm.Apply(m)
	m.PendingMove = nil
	return nil
}

// reportMoveError prints the right guidance for a failed move/fixup: a conflict
// leaves a rebase in progress; anything else left the repo untouched.
func reportMoveError(err error) {
	var rebuildErr rebuildConflictError
	if errors.As(err, &rebuildErr) {
		printMoveFailure(err)
	} else {
		fmt.Printf("Error: %v\n", err)
	}
}

// reconcilePending settles a PendingMove or PendingRebase left by a conflicted
// move/fixup/rebase once the user has finished or aborted the git rebase, per
// DESIGN.md. It reports whether the rebase is still in progress.
func reconcilePending() (inProgress bool) {
	m, err := metadata.Load()
	if err != nil || (m.PendingMove == nil && m.PendingRebase == nil) {
		return false
	}
	if git.RebaseInProgress() {
		return true
	}
	if m.PendingRebase != nil {
		settlePendingRebase(m)
		return false
	}
	_, _, since, err := git.ResolveBaseBranch()
	if err != nil {
		return false // can't tell where we are; leave the intent for next time
	}
	settlePendingMove(m, since)
	return false
}

// settlePendingRebase finishes the bookkeeping for a conflicted rebase that is
// no longer in progress. It was continued iff HEAD moved, the updated base is
// an ancestor of HEAD, and none of the commits dropped as landed is still in
// history; otherwise it was aborted (or never started) and the backup taken
// before it is discarded.
func settlePendingRebase(m *metadata.Metadata) {
	pr := m.PendingRebase
	continued := false
	if head, err := git.RevParse("HEAD"); err == nil && head != pr.OrigHead && git.IsAncestor(pr.TargetTip, head) {
		continued = true
		if commits, err := git.GetCommitsInRange(pr.TargetTip); err == nil {
			present := map[string]bool{}
			for _, id := range commitIDs(commits) {
				present[id] = true
			}
			for _, id := range pr.DropIDs {
				if present[id] {
					continued = false
				}
			}
		}
	}
	m.PendingRebase = nil
	if !continued {
		discardBackup(m, pr.BackupRef)
		if err := m.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save metadata: %v\n", err)
		}
		return
	}
	retired, err := finishRebase(m, pr.TargetTip, pr.RetireIDs, pr.DropIDs, pr.Synthetics)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not finish the rebase bookkeeping: %v\n", err)
		return
	}
	fmt.Printf("Finished the rebase: retired %d commit(s).\n", retired)
}

// settlePendingMove applies or discards m's PendingMove (no rebase in
// progress) according to the branch's current order since `since`, and saves.
func settlePendingMove(m *metadata.Metadata, since string) {
	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		return
	}
	pm := m.PendingMove
	switch cur := commitIDs(commits); {
	case slices.Equal(cur, pm.NewOrder):
		pm.Apply(m)
	case slices.Equal(cur, pm.OldOrder):
		// Aborted (or never started): nothing to record.
	default:
		fmt.Fprintf(os.Stderr, "Warning: commits were changed while resolving the move; the attachment of %s was not recorded.\n", pm.MovedID)
	}
	m.PendingMove = nil
	if err := m.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save metadata: %v\n", err)
	}
}
