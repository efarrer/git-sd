package main

import (
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/metadata"
	"os"
	"strings"
)

func runMove(args []string) {
	if len(args) == 0 {
		fmt.Println("Error: missing move string (e.g., 3:1, or 3:1.1)")
		os.Exit(1)
	}

	srcAddr, destAddr, ok := strings.Cut(args[0], ":")
	if !ok {
		fmt.Printf("Error: invalid move format '%s'\n", args[0])
		os.Exit(1)
	}

	srcIdxArg, srcRankArg, err := parseAddress(srcAddr)
	if err != nil {
		fmt.Printf("Error: invalid source '%s': %v\n", srcAddr, err)
		os.Exit(1)
	}
	destIdxArg, newRank, err := parseAddress(destAddr)
	if err != nil {
		fmt.Printf("Error: invalid destination '%s': %v\n", destAddr, err)
		os.Exit(1)
	}

	_, _, since, err := git.ResolveBaseBranch()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Ensure IDs are present (mirrors 'assign') so the moved commit always has
	// a git-sd-id to record its new ParentID/SubRank/StackSet against.
	if err := git.RewriteCommitsWithIDs(since); err != nil {
		fmt.Printf("Error tagging commits: %v\n", err)
		os.Exit(1)
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		fmt.Printf("Error getting commits: %v\n", err)
		os.Exit(1)
	}

	if destIdxArg < 0 || destIdxArg >= len(commits) {
		fmt.Println("Error: index out of range")
		os.Exit(1)
	}
	m, err := metadata.Load()
	if err != nil {
		fmt.Printf("Error loading metadata: %v\n", err)
		os.Exit(1)
	}

	srcIdx, _, err := resolveExisting(commits, m, srcIdxArg, srcRankArg)
	if err != nil {
		fmt.Printf("Error: invalid source: %v\n", err)
		os.Exit(1)
	}
	if srcIdx == destIdxArg {
		fmt.Println("Error: source and destination are the same commit")
		os.Exit(1)
	}

	if newRank == "" {
		// Plain move: reorder to destIdxArg as a top-level commit. If src was
		// a sub-commit, it's promoted -- ParentID/SubRank clear, but
		// StackSet is left as-is (promotion doesn't remove it from the Stack
		// Set it was already contributing to).
		if err := reorderCommits(since, commits, m, srcIdx, destIdxArg); err != nil {
			reportMoveError(err)
			os.Exit(1)
		}
	} else {
		if err := attachAsSubCommit(since, commits, m, srcIdx, destIdxArg, newRank); err != nil {
			reportMoveError(err)
			os.Exit(1)
		}
	}

	if err := m.Save(); err != nil {
		fmt.Printf("Error saving metadata: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Commits reordered successfully.")
}

// rebuildConflictError marks an error as coming from RebuildCommitChainRebase
// (a real cherry-pick conflict, repo left mid-rebuild) rather than a plain
// validation failure (repo untouched) -- so callers can print the right
// guidance for each.
type rebuildConflictError struct{ err error }

func (e rebuildConflictError) Error() string { return e.err.Error() }
func (e rebuildConflictError) Unwrap() error { return e.err }

func printMoveFailure(err error) {
	fmt.Printf("Error: Conflict while reordering.\n")
	fmt.Printf("Details: %v\n", err)
	fmt.Println("Resolve it, then run 'git rebase --continue' to finish, or 'git rebase --abort' to restore the original order.")
	fmt.Println("'git sd status' will pick up whichever you chose.")
}

// reorderCommits removes the commit at srcIdx and reinserts it at destIdx
// (interpreted as a slot in the post-removal slice), rebuilding the branch
// to match. If srcIdx is a top-level commit with its own sub-commits, the
// whole group (parent + subs, in their existing relative order) moves
// together as one block -- their ParentID/SubRank need no changes, since a
// sub-commit's displayed label is always derived from its parent's CURRENT
// physical position (see status.go), so relocating the parent alone already
// updates every child's label the next time status runs.
func reorderCommits(since string, commits []git.Commit, m *metadata.Metadata, srcIdx, destIdx int) error {
	c := commits[srcIdx]
	movedID := git.ExtractTrailer(c.Message, "git-sd-id")

	block := []git.Commit{c}
	inBlock := map[string]bool{movedID: true}
	if m.Commits[movedID].ParentID == "" {
		for _, rc := range commits {
			rid := git.ExtractTrailer(rc.Message, "git-sd-id")
			if rid == "" || rid == movedID {
				continue
			}
			if cm, ok := m.Commits[rid]; ok && cm.ParentID == movedID {
				block = append(block, rc)
				inBlock[rid] = true
			}
		}
	}

	var reduced []git.Commit
	for _, rc := range commits {
		rid := git.ExtractTrailer(rc.Message, "git-sd-id")
		if inBlock[rid] {
			continue
		}
		reduced = append(reduced, rc)
	}
	if destIdx > len(reduced) {
		destIdx = len(reduced)
	}

	newCommits := make([]git.Commit, 0, len(reduced)+len(block))
	newCommits = append(newCommits, reduced[:destIdx]...)
	newCommits = append(newCommits, block...)
	newCommits = append(newCommits, reduced[destIdx:]...)
	return rebuildWithIntent(m, since, commits, newCommits, movedID, "", "", m.Commits[movedID].StackSet)
}

// attachAsSubCommit reorders the commit at srcIdx to become a sub-commit of
// the top-level commit at targetIdx with the given rank, physically
// repositioning it immediately after the target's existing sub-commit with
// the largest rank less than the new one (or after the target itself if
// none), and records the resulting ParentID/SubRank/StackSet (always
// inherited from the target -- the target's StackSet may be "", meaning the
// attachment is dormant until the target itself gets assigned). Shared by
// 'move's ranked-destination form and 'fixup'. Does not save metadata --
// callers do that once they're done.
func attachAsSubCommit(since string, commits []git.Commit, m *metadata.Metadata, srcIdx, targetIdx int, rank string) error {
	if srcIdx == targetIdx {
		return fmt.Errorf("source and target are the same commit")
	}
	targetID := git.ExtractTrailer(commits[targetIdx].Message, "git-sd-id")
	targetCM := m.Commits[targetID]
	if targetCM.ParentID != "" {
		return fmt.Errorf("cannot attach under index %d — it is itself a sub-commit; sub-commits cannot have their own sub-commits", targetIdx)
	}

	c := commits[srcIdx]
	movedID := git.ExtractTrailer(c.Message, "git-sd-id")
	if m.Commits[movedID].ParentID == "" {
		for id, cm := range m.Commits {
			if cm.ParentID == movedID {
				return fmt.Errorf("commit at index %d already has its own sub-commit (%s) — attaching it elsewhere would orphan it; move that sub-commit first, or promote/relocate this group as a whole", srcIdx, id)
			}
		}
	}

	reduced := append(append([]git.Commit{}, commits[:srcIdx]...), commits[srcIdx+1:]...)
	insertAfter := -1
	for i, rc := range reduced {
		rid := git.ExtractTrailer(rc.Message, "git-sd-id")
		if rid == targetID {
			insertAfter = i
			continue
		}
		rcm := m.Commits[rid]
		if rcm.ParentID == targetID && rankLess(rcm.SubRank, rank) && i > insertAfter {
			insertAfter = i
		}
	}
	if insertAfter == -1 {
		return fmt.Errorf("target commit at index %d not found", targetIdx)
	}
	destIdx := insertAfter + 1

	newCommits := make([]git.Commit, 0, len(reduced)+1)
	newCommits = append(newCommits, reduced[:destIdx]...)
	newCommits = append(newCommits, c)
	newCommits = append(newCommits, reduced[destIdx:]...)

	return rebuildWithIntent(m, since, commits, newCommits, movedID, targetID, rank, targetCM.StackSet)
}
