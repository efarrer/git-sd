package main

import (
	"fmt"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/metadata"
	"strconv"
)

// runFixup is sugar for attaching HEAD as the next sub-commit of <target>:
// 'git sd fixup <target>' == 'git sd move <index of HEAD>:<target>.<computed-rank>'.
// See SPEC.md's "git sd fixup" section.
func runFixup(args []string) {
	if len(args) != 1 {
		fmt.Println("Error: usage: git sd fixup <target-index>")
		exitCleanly(1)
	}
	targetIdx, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Printf("Error: invalid target index '%s'\n", args[0])
		exitCleanly(1)
	}

	_, _, since, err := git.ResolveBaseBranch()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}

	if err := git.RewriteCommitsWithIDs(since); err != nil {
		fmt.Printf("Error tagging commits: %v\n", err)
		exitCleanly(1)
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		fmt.Printf("Error getting commits: %v\n", err)
		exitCleanly(1)
	}
	if len(commits) == 0 {
		fmt.Println("Error: no local commits to fix up")
		exitCleanly(1)
	}
	srcIdx := len(commits) - 1 // HEAD
	if targetIdx < 0 || targetIdx >= len(commits) {
		fmt.Println("Error: target index out of range")
		exitCleanly(1)
	}
	if targetIdx == srcIdx {
		fmt.Println("Error: HEAD is already the target commit")
		exitCleanly(1)
	}

	m, err := metadata.Load()
	if err != nil {
		fmt.Printf("Error loading metadata: %v\n", err)
		exitCleanly(1)
	}

	// (attachAsSubCommit rejects HEAD already having its own sub-commits,
	// or the target itself being a sub-commit, uniformly for both 'move'
	// and 'fixup'.)
	targetID := git.ExtractTrailer(commits[targetIdx].Message, "git-sd-id")
	targetCM := m.Commits[targetID]
	if targetCM.StackSet == "" {
		fmt.Println("Error: target commit is not assigned to a Stack Set yet — assign it first.")
		exitCleanly(1)
	}

	var siblingRanks []string
	for _, cm := range m.Commits {
		if cm.ParentID == targetID {
			siblingRanks = append(siblingRanks, cm.SubRank)
		}
	}
	rank := nextRank(siblingRanks)

	if err := attachAsSubCommit(since, commits, m, srcIdx, targetIdx, rank); err != nil {
		reportMoveError(err)
		exitCleanly(1)
	}

	if err := m.Save(); err != nil {
		fmt.Printf("Error saving metadata: %v\n", err)
		exitCleanly(1)
	}

	fmt.Printf("Attached as sub-commit %d.%s.\n", targetIdx, rank)
}
