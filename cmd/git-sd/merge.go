package main

import (
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/github"
	"git-sd/internal/metadata"
	"strconv"
)

func runMerge(args []string) {
	setName := ""
	all := false
	uptoIdx := -1 // -1 = no limit; otherwise merge only the bottom prefix up to this top-level index
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			all = true
		case "--upto":
			i++
			if i >= len(args) {
				fmt.Println("Error: --upto requires an index (e.g. 'git sd merge <set> --upto 1')")
				exitCleanly(1)
			}
			n, cerr := strconv.Atoi(args[i])
			if cerr != nil || n < 0 {
				fmt.Printf("Error: invalid --upto index '%s' (must be a non-negative integer)\n", args[i])
				exitCleanly(1)
			}
			uptoIdx = n
		default:
			setName = args[i]
		}
	}

	if !all && setName == "" {
		fmt.Println("Error: must specify a stack set to merge, or use --all")
		exitCleanly(1)
	}
	if all && uptoIdx >= 0 {
		fmt.Println("Error: --upto cannot be combined with --all (it limits a single set's prefix)")
		exitCleanly(1)
	}

	beginOperation("merge", setName) // lets `status` in another terminal show Syncing
	defer endOperation()

	m, err := metadata.Load()
	if err != nil {
		fmt.Printf("Error loading metadata: %v\n", err)
		exitCleanly(1)
	}

	_, _, since, err := git.ResolveBaseBranch()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		fmt.Printf("Error getting commits: %v\n", err)
		exitCleanly(1)
	}

	// merge is GitHub-only: it never touches the local branch or working
	// tree. 'git sd rebase' drops the merged commits afterwards, verifying
	// first that their content actually landed.
	anySucceeded := false
	if !all {
		if err := mergeStackSet(m, commits, setName, uptoIdx); err != nil {
			fmt.Printf("Error: %v\n", err)
			exitCleanly(1)
		}
		anySucceeded = true
	} else {
		setNames := m.StackSetNames()
		if len(setNames) == 0 {
			fmt.Println("No stack sets found to merge.")
			return
		}
		for _, sn := range setNames {
			if err := mergeStackSet(m, commits, sn, -1); err != nil {
				fmt.Printf("Skipping stack set '%s': %v\n", sn, err)
				continue
			}
			anySucceeded = true
		}
		if !anySucceeded {
			exitCleanly(1)
		}
	}

	if err := m.Save(); err != nil {
		fmt.Printf("Error saving metadata: %v\n", err)
		exitCleanly(1)
	}
	if anySucceeded {
		fmt.Println("Run 'git sd rebase' to drop the merged commits locally.")
	}
}

// mergeStackSet merges a single named stack set via GitHub's native async
// stack merge: it finds the top-most PR at or below --upto's cap and submits
// just that PR (github.MergeStack merges every PR in the chain up to and
// including it — including approval/CI safeguards, which are now enforced
// entirely by GitHub's branch protection rather than a pre-merge check
// git-sd runs itself). It does NOT modify the local branch or the merged
// commits' metadata — 'git sd rebase' drops and retires them afterwards.
// uptoIdx (>=0) limits the target to a top-level commit whose physical index
// (as shown in `git sd status`) is <= uptoIdx; -1 means no limit.
func mergeStackSet(m *metadata.Metadata, commits []git.Commit, setName string, uptoIdx int) error {
	// Physical index (as shown in status) of each commit, for --upto capping.
	physIdx := map[string]int{}
	for i, c := range commits {
		physIdx[c.Hash] = i
	}

	// Group into PRs and keep only this set's groups, in order.
	var setGroups []PRGroup
	for _, g := range GroupCommits(commits, m) {
		if g.StackSet == setName {
			setGroups = append(setGroups, g)
		}
	}
	if len(setGroups) == 0 {
		return fmt.Errorf("no PRs found for stack set '%s'", setName)
	}

	// Find the top-most group at or below --upto's cap that has a PR.
	targetPos := -1
	for pos, g := range setGroups {
		// Respect --upto: groups are in ascending top-level index order, so once
		// a group's top-level commit is past the cap, stop — everything above is too.
		if uptoIdx >= 0 && physIdx[g.Commits[0].Hash] > uptoIdx {
			break
		}
		if m.Commits[g.TopLevelID].PRNumber != 0 {
			targetPos = pos
		}
	}
	if targetPos == -1 {
		if uptoIdx >= 0 {
			return fmt.Errorf("no PR found for stack set '%s' at or below index %d", setName, uptoIdx)
		}
		return fmt.Errorf("no PRs found for stack set '%s'", setName)
	}

	// GitHub's async merge cascades every PR from the bottom of the
	// registered Stack up through the target — every group in that range
	// must already have a PR (i.e. be synced), or the Stack's chain has a
	// gap the API doesn't know how to bridge.
	for pos := 0; pos <= targetPos; pos++ {
		if m.Commits[setGroups[pos].TopLevelID].PRNumber == 0 {
			return fmt.Errorf("stack set '%s' has an un-synced group below the merge target; run 'git sd sync %s' first", setName, setName)
		}
	}

	targetGroup := setGroups[targetPos]
	targetMetadata := m.Commits[targetGroup.TopLevelID]
	fmt.Printf("Merging stack set '%s' up to PR #%d...\n", setName, targetMetadata.PRNumber)

	if err := github.MergeStack(targetMetadata.PRNumber); err != nil {
		return fmt.Errorf("error merging PR #%d: %w", targetMetadata.PRNumber, err)
	}

	// Deliberately leave the Stack Set (and its metadata) as-is: the merged
	// commits are still on the local branch until 'git sd rebase' retires
	// them, and a partial merge keeps m.StackSets[setName].StackNumber for
	// the next sync's Pass 0 to unstack (GitHub's Stacks API leaves in place
	// whatever couldn't be unstacked — merged/merging/queued PRs). sync
	// dissolves a set once every commit in it has been retired.

	fmt.Println("Merge completed successfully.")
	return nil
}
