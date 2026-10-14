package main

import (
	"fmt"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/metadata"
	"strconv"
	"strings"
)

func runAssign(args []string) {
	if len(args) == 0 {
		fmt.Println("Error: missing assignment string (e.g., feat-a:1,3-5)")
		exitCleanly(1)
	}

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

	// Ensure IDs are present
	if err := git.RewriteCommitsWithIDs(since); err != nil {
		fmt.Printf("Error tagging commits: %v\n", err)
		exitCleanly(1)
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		fmt.Printf("Error getting commits: %v\n", err)
		exitCleanly(1)
	}

	anyFailed := false

	for _, arg := range args {
		parts := strings.Split(arg, ":")
		if len(parts) != 2 {
			fmt.Printf("Error: invalid assignment format '%s'\n", arg)
			anyFailed = true
			continue
		}

		setName := parts[0]
		indexRange := parts[1]

		// Snapshot every commit id this argument touches before mutating, so
		// a failed dependency check below can cleanly roll back just this
		// argument's changes without disturbing other arguments in the same
		// call or previously-saved state.
		snapshot := map[string]metadata.CommitMetadata{}
		touch := func(id string) {
			if _, ok := snapshot[id]; !ok {
				snapshot[id] = m.Commits[id]
			}
		}

		for idxRange := range strings.SplitSeq(indexRange, ",") {
			if strings.Contains(idxRange, ".") {
				// Hierarchical address (e.g. 1.1): assign can never use
				// hierarchical addresses all sub-commits always intherit
				// the assignment from the parent commit.
				// See SPEC.md's assign section.
				if setName != "" {
					fmt.Printf("Error: '%s' looks like a sub-commit address (N.R) — assign never attaches or reparents a sub-commit; use 'git sd move %s:<dest>' to promote it to top-level first, then assign it separately, or 'git sd fixup'/'git sd move ...:<dest>.<rank>' to attach/reparent directly.\n", idxRange, idxRange)
					anyFailed = true
					continue
				}
				idx, rank, perr := parseAddress(idxRange)
				if perr != nil {
					fmt.Printf("Error: %v\n", perr)
					anyFailed = true
					continue
				}
				_, id, rerr := resolveExisting(commits, m, idx, rank)
				if rerr != nil {
					fmt.Printf("Error: %v\n", rerr)
					anyFailed = true
					continue
				}
				touch(id)
				cm := m.Commits[id]
				// Full detach: never inconsistent, always allowed.
				cm.StackSet = ""
				cm.ParentID = ""
				cm.SubRank = ""
				m.Commits[id] = cm
			} else {
				// Standard range assignment (top-level commits only).
				indices, err := parseIndexRange(idxRange)
				if err != nil {
					fmt.Printf("Error parsing range '%s': %v\n", idxRange, err)
					anyFailed = true
					continue
				}

				for _, idx := range indices {
					if idx < 0 || idx >= len(commits) {
						fmt.Printf("Error: index %d out of range\n", idx)
						anyFailed = true
						continue
					}

					id := git.ExtractTrailer(commits[idx].Message, "git-sd-id")
					touch(id)
					cm := m.Commits[id]
					cm.StackSet = setName
					cm.ParentID = ""
					cm.SubRank = ""
					m.Commits[id] = cm
					cascadeStackSet(m, id, setName)
				}
			}
		}

		// A new top-level commit whose content depends on another Stack
		// Set's change (or on an unassigned ancestor) would leave setName
		// unable to sync — 'sync' only ever rebuilds a Stack Set's own
		// commits onto the base branch, chained sequentially among
		// themselves, never onto another set's content. Catch that now
		// rather than as a confusing cherry-pick conflict during 'sync'.
		// Skip for unassignment (setName == "").
		if setName != "" {
			if problem := validateStackSetSyncable(commits, m, since, setName); problem != "" {
				fmt.Printf("Error: %s\n", problem)
				for id, old := range snapshot {
					m.Commits[id] = old
				}
				anyFailed = true
			}
		}
	}

	if err := m.Save(); err != nil {
		fmt.Printf("Error saving metadata: %v\n", err)
		exitCleanly(1)
	}

	if anyFailed {
		fmt.Println("Some assignments failed; see errors above.")
		exitCleanly(1)
	}
	fmt.Println("Commits assigned successfully.")
}

// validateStackSetSyncable reports (as a non-empty message) whether setName's
// own commits, replayed in physical order via git.SimulateChainBuild, would
// fail to rebuild cleanly onto since — mirroring exactly what 'sync' does at
// push time (BuildTrailerStrippedChain, chained across the set's own groups
// only). Returns "" if setName is sync-safe (or has no commits at all).
func validateStackSetSyncable(commits []git.Commit, m *metadata.Metadata, since, setName string) string {
	var setCommits []git.Commit
	for _, g := range GroupCommits(commits, m) {
		if g.StackSet == setName {
			setCommits = append(setCommits, g.Commits...)
		}
	}
	if len(setCommits) == 0 {
		return ""
	}

	failedAt, err := git.SimulateChainBuild(since, setCommits)
	if err != nil {
		return fmt.Sprintf("could not validate stack set '%s': %v", setName, err)
	}
	if failedAt == -1 {
		return ""
	}
	culprit := setCommits[failedAt]

	// Best-effort hint: the most common cause is that the culprit's actual
	// physical (git) parent belongs to a different Stack Set — find it in
	// the full commit list (not just setName's own, filtered commits).
	hint := ""
	for i, c := range commits {
		if c.Hash != culprit.Hash || i == 0 {
			continue
		}
		if pid := git.ExtractTrailer(commits[i-1].Message, "git-sd-id"); pid != "" {
			if pcm, ok := m.Commits[pid]; ok && pcm.StackSet != "" && pcm.StackSet != setName {
				hint = fmt.Sprintf(" — it likely depends on a change in Stack Set '%s' (commit %s)", pcm.StackSet, commits[i-1].Hash[:7])
			}
		}
		break
	}

	return fmt.Sprintf("assigning to '%s' would leave it unable to sync — commit %s does not apply cleanly on its own%s. Put dependent commits in the same Stack Set together.", setName, culprit.Hash[:7], hint)
}

// cascadeStackSet re-stamps StackSet on every direct sub-commit of parentID
// to newSet (including "" when unassigning) -- a sub-commit's StackSet
// always mirrors its parent's, so reassigning (or unassigning) the parent
// cascades to its children automatically. Their ParentID/SubRank are left
// untouched.
func cascadeStackSet(m *metadata.Metadata, parentID, newSet string) {
	for id, cm := range m.Commits {
		if cm.ParentID == parentID {
			cm.StackSet = newSet
			m.Commits[id] = cm
		}
	}
}

func parseIndexRange(r string) ([]int, error) {
	var result []int
	for p := range strings.SplitSeq(r, ",") {
		if strings.Contains(p, "-") {
			rangeParts := strings.Split(p, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range '%s'", p)
			}
			start, _ := strconv.Atoi(rangeParts[0])
			end, _ := strconv.Atoi(rangeParts[1])
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
		} else {
			idx, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid index '%s'", p)
			}
			result = append(result, idx)
		}
	}
	return result, nil
}
