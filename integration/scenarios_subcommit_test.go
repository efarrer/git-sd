package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDSubCommitFoldsIntoParentPRNotItsOwn verifies the sub-commit
// (PR-feedback) workflow that the index-based branch scheme used to corrupt: a
// bug-fix commit attached to a top-level commit must fold into that commit's PR
// (not spawn its own), and the downstream PR stays its own separate PR.
//
// It starts by resetting the local branch to origin/main so it isn't fighting
// accumulated commits from earlier scenarios.
func TestSDSubCommitFoldsIntoParentPRNotItsOwn(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Sub-commit Folding ---")

	// Isolate from earlier scenarios' accumulated local commits.
	runGit(t, repoDir, "fetch", "origin")
	runGit(t, repoDir, "reset", "--hard", "origin/main")

	// 2-commit stack, one PR each.
	for _, c := range []struct{ file, msg string }{
		{"subfix-c1.txt", "Subfix Commit 1"},
		{"subfix-c2.txt", "Subfix Commit 2"},
	} {
		if err := os.WriteFile(filepath.Join(repoDir, c.file), fmt.Appendf(nil, "%s %d", c.msg, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", c.file)
		runGit(t, repoDir, "commit", "-m", c.msg)
	}
	runGitSD(t, repoDir, "assign", "subfix:0-1")
	runGitSD(t, repoDir, "status")
	runGitSD(t, repoDir, "sync")
	out := runGitSD(t, repoDir, "status")
	if n := len(extractStackPRNumbers(out, "subfix")); n != 2 {
		t.Fatalf("[sub-commit] expected 2 PRs for the initial stack, got %d.\nStatus:\n%s", n, out)
	}
	fmt.Println("[sub-commit] baseline 2-PR stack created.")

	// Add a bug-fix and attach it as a sub-commit of "Subfix Commit 1" (index
	// 0). With the fix, this both repositions it and joins it to that
	// commit's Stack Set.
	if err := os.WriteFile(filepath.Join(repoDir, "subfix-bug.txt"), fmt.Appendf(nil, "bug fix %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "subfix-bug.txt")
	runGit(t, repoDir, "commit", "-m", "Subfix Bugfix")
	runGitSD(t, repoDir, "track") // assign an id, populate indices
	runGitSD(t, repoDir, "move", "2:0.1")
	out = runGitSD(t, repoDir, "status")
	// The bug-fix row must show sub-index 0.1 and belong to subfix.
	sawSub := false
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Subfix Bugfix") {
			if !strings.Contains(line, "0.1") || !strings.Contains(line, "subfix") {
				t.Fatalf("[sub-commit] bug-fix not attached as subfix sub-commit 0.1. Line: %s", line)
			}
			sawSub = true
		}
	}
	if !sawSub {
		t.Fatalf("[sub-commit] could not find the bug-fix row in status:\n%s", out)
	}
	fmt.Println("[sub-commit] bug-fix attached as sub-commit 0.1 of commit 1.")

	// Sync again: the bug-fix must fold into PR1 (no third PR), and PR2 must
	// stay its own PR based on PR1's branch.
	runGitSD(t, repoDir, "sync")
	out = runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "subfix")
	if len(prs) != 2 {
		t.Fatalf("[sub-commit] regression: expected still 2 PRs after the sub-commit (it must fold into PR1, not get its own PR), got %d.\nStatus:\n%s", len(prs), out)
	}
	// PR1 (top of the set in status order) must contain BOTH commit 1 and the bug-fix.
	runGit(t, repoDir, "fetch", "origin")
	pr1Head := prHeadRef(t, repoDir, prs[0])
	pr1Log := runGit(t, repoDir, "log", "--format=%s", "origin/main..origin/"+pr1Head)
	if !strings.Contains(pr1Log, "Subfix Commit 1") || !strings.Contains(pr1Log, "Subfix Bugfix") {
		t.Fatalf("[sub-commit] PR1 branch %q must contain commit 1 AND the bug-fix; got:\n%s", pr1Head, pr1Log)
	}
	// PR2's diff must show only commit 2's file (correct stacking, no leaked commit1/bug-fix changes).
	pr2Diff := runGit(t, repoDir, "pr", "diff", prs[1], "--name-only")
	if !strings.Contains(pr2Diff, "subfix-c2.txt") || strings.Contains(pr2Diff, "subfix-c1.txt") || strings.Contains(pr2Diff, "subfix-bug.txt") {
		t.Fatalf("[sub-commit] PR2 diff should be only subfix-c2.txt; got:\n%s", pr2Diff)
	}
	fmt.Println("[sub-commit] bug-fix folded into PR1; PR2 remains its own clean PR.")
}

// TestSDMergeSquashesSubCommitAndDropsMergedCommitsLocally exercises a
// squash-merge of a sub-commit group + local drop, using a single-PR group
// (which merges on the sacrificial repo). See
// TestSDPartialMergeOfSubCommitPRLeavesDownstreamClean for the 2-PR partial
// merge (driven via `merge --upto`).
func TestSDMergeSquashesSubCommitAndDropsMergedCommitsLocally(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Sub-commit Squash-Merge + Local Drop ---")
	runGit(t, repoDir, "fetch", "origin")
	runGit(t, repoDir, "reset", "--hard", "origin/main")
	for _, c := range []struct{ file, msg string }{
		{"subm-a.txt", "Subm A"},
		{"subm-bug.txt", "Subm Bugfix"},
	} {
		if err := os.WriteFile(filepath.Join(repoDir, c.file), fmt.Appendf(nil, "%s %d", c.msg, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", c.file)
		runGit(t, repoDir, "commit", "-m", c.msg)
	}
	runGitSD(t, repoDir, "assign", "subm:0") // commit A top-level
	runGitSD(t, repoDir, "status")
	// subm-bug is already HEAD (the most recent commit), so `fixup` attaches
	// it as the next sub-commit of subm-a (index 0) directly — no manual
	// repositioning needed. The subfix scenario above covers the manual
	// move-to-sub-index path.
	runGitSD(t, repoDir, "fixup", "0") // bug-fix as sub-commit of A
	runGitSD(t, repoDir, "status")
	runGitSD(t, repoDir, "sync")
	out := runGitSD(t, repoDir, "status")
	submPRs := extractStackPRNumbers(out, "subm")
	if len(submPRs) != 1 {
		t.Fatalf("[sub-merge] expected exactly 1 PR for a top-level + sub-commit group, got %d.\nStatus:\n%s", len(submPRs), out)
	}
	fmt.Println("[sub-merge] top-level + sub-commit produced a single PR.")

	runGitErr(repoDir, "pr", "review", submPRs[0], "--approve") // best-effort; own PR can't be self-approved
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "subm")
	if mergeErr != nil {
		t.Fatalf("[sub-merge] git sd merge failed: %v\nOutput: %s", mergeErr, mergeOut)
	}
	fmt.Println(mergeOut)
	// merge is GitHub-only; the local drop happens in rebase.
	runGitSD(t, repoDir, "rebase")
	// Local drop: the merged commits must be gone from the local branch.
	localLog := runGit(t, repoDir, "log", "--format=%s", "origin/main..HEAD")
	if strings.Contains(localLog, "Subm A") || strings.Contains(localLog, "Subm Bugfix") {
		t.Fatalf("[sub-merge] merged commits were not dropped from the local branch; local log:\n%s", localLog)
	}
	fmt.Println("[sub-merge] squash-merge folded the sub-commit and dropped the merged commits locally.")
	runGitSDErr(repoDir, "sync", "subm")
}

// TestSDPartialMergeOfSubCommitPRLeavesDownstreamClean covers TODO #3: a 2-PR
// stack where PR0 has a commit + a bug-fix sub-commit, PR0 is squash-merged
// (via `merge --upto 0`, so only PR0 merges while PR1 survives), and PR1 then
// rebases onto main carrying ONLY its own commit — not the merged commit or
// bug-fix. Uses --upto because `git sd merge` is otherwise top-mergeable and
// can't merge just PR0, and the private free-plan repo can't gate merges by
// review.
func TestSDPartialMergeOfSubCommitPRLeavesDownstreamClean(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Partial merge of a sub-commit PR ---")
	runGit(t, repoDir, "fetch", "origin")
	runGit(t, repoDir, "reset", "--hard", "origin/main")

	// 2-commit stack.
	for _, c := range []struct{ file, msg string }{
		{"pm3-c1.txt", "PM3 Commit 1"},
		{"pm3-c2.txt", "PM3 Commit 2"},
	} {
		if err := os.WriteFile(filepath.Join(repoDir, c.file), fmt.Appendf(nil, "%s %d", c.msg, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", c.file)
		runGit(t, repoDir, "commit", "-m", c.msg)
	}
	runGitSD(t, repoDir, "assign", "pm3:0-1")
	runGitSD(t, repoDir, "status")
	runGitSD(t, repoDir, "sync")

	// Bug-fix folded into PR0 (commit 1).
	if err := os.WriteFile(filepath.Join(repoDir, "pm3-bug.txt"), fmt.Appendf(nil, "bug %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "pm3-bug.txt")
	runGit(t, repoDir, "commit", "-m", "PM3 Bugfix")
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "move", "2:0.1") // bug-fix as sub-commit of "PM3 Commit 1" (index 0)
	runGitSD(t, repoDir, "sync")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "pm3")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs (bug-fix folded into PR0), got %d.\nStatus:\n%s", len(prs), out)
	}
	pr0, pr1 := prs[0], prs[1]

	// Merge ONLY PR0 (commit 1 + bug-fix, squashed) via --upto 0.
	mout, merr := runGitSDErr(repoDir, "merge", "pm3", "--upto", "0")
	if merr != nil {
		t.Fatalf("git sd merge pm3 --upto 0 failed: %v\nOutput: %s", merr, mout)
	}
	// merge is GitHub-only: drop PR0's merged commits locally first, then
	// retarget the surviving PR1 onto main and push the rebased commit 2.
	runGitSD(t, repoDir, "rebase")
	runGitSD(t, repoDir, "sync")

	// PR0 merged.
	pr0View := runGit(t, repoDir, "pr", "view", pr0, "--json", "state")
	var pr0Info struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(pr0View), &pr0Info); err != nil {
		t.Fatalf("failed to parse PR #%s state: %v", pr0, err)
	}
	if pr0Info.State != "MERGED" {
		t.Fatalf("expected PR0 #%s to be MERGED, got %s", pr0, pr0Info.State)
	}

	// PR1 open, based on main, carrying ONLY commit 2 — not commit 1 or the bug-fix.
	pr1View := runGit(t, repoDir, "pr", "view", pr1, "--json", "state,baseRefName,headRefName")
	var pr1Info struct {
		State       string `json:"state"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(pr1View), &pr1Info); err != nil {
		t.Fatalf("failed to parse PR #%s info: %v", pr1, err)
	}
	if pr1Info.State != "OPEN" {
		t.Fatalf("expected PR1 #%s to remain OPEN after partial merge, got %s", pr1, pr1Info.State)
	}
	if pr1Info.BaseRefName != "main" {
		t.Fatalf("expected PR1 #%s to retarget to main, got base %q", pr1, pr1Info.BaseRefName)
	}
	runGit(t, repoDir, "fetch", "origin")
	pr1Log := runGit(t, repoDir, "log", "--format=%s", "origin/main..origin/"+pr1Info.HeadRefName)
	if !strings.Contains(pr1Log, "PM3 Commit 2") {
		t.Fatalf("PR1 branch should contain 'PM3 Commit 2'; got:\n%s", pr1Log)
	}
	if strings.Contains(pr1Log, "PM3 Commit 1") || strings.Contains(pr1Log, "PM3 Bugfix") {
		t.Fatalf("PR1 branch must NOT re-carry the merged commit/bug-fix (TODO #3); got:\n%s", pr1Log)
	}
	fmt.Println("Partial merge of sub-commit PR verified: PR0 merged, PR1 clean on main.")
	runGitSDErr(repoDir, "sync", "pm3")
}

// TestSDFixupAttachesHeadToExistingPR verifies the basic `fixup` workflow:
// a fix commit made after a PR already exists (landing at HEAD) attaches to
// that PR without any manual index/rank computation.
func TestSDFixupAttachesHeadToExistingPR(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "fixupbasic", "FixupBasic Commit", 1)
	runGitSD(t, repoDir, "assign", "fixupbasic:0")
	runGitSD(t, repoDir, "sync")

	out := runGitSD(t, repoDir, "status")
	prsBefore := extractStackPRNumbers(out, "fixupbasic")
	if len(prsBefore) != 1 {
		t.Fatalf("expected 1 PR before fixup, got %d. Status:\n%s", len(prsBefore), out)
	}

	filename := filepath.Join(repoDir, "fixupbasic-fix.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "fix %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "FixupBasic Fix")

	fout := runGitSD(t, repoDir, "fixup", "0")
	if !strings.Contains(fout, "0.1") {
		t.Fatalf("expected fixup to report attaching as 0.1, got: %s", fout)
	}

	out = runGitSD(t, repoDir, "status")
	var fixLine string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "FixupBasic Fix") {
			fixLine = line
		}
	}
	if !strings.Contains(fixLine, "0.1") || !strings.Contains(fixLine, "fixupbasic") {
		t.Fatalf("expected fixup commit to show as 0.1 under fixupbasic, got line: %s", fixLine)
	}

	runGitSD(t, repoDir, "sync")
	out = runGitSD(t, repoDir, "status")
	prsAfter := extractStackPRNumbers(out, "fixupbasic")
	if len(prsAfter) != 1 || prsAfter[0] != prsBefore[0] {
		t.Fatalf("expected fixup to fold into the same single PR #%s, got %v", prsBefore[0], prsAfter)
	}
	fmt.Println("fixup verified: HEAD attached to existing PR without manual index math.")
}

// TestSDFixupAutoTracksUntrackedHead verifies fixup self-tracks HEAD (the
// same way move/assign do) when it has no git-sd-id yet -- no separate
// 'git sd track' step is required before fixing up a fresh commit.
func TestSDFixupAutoTracksUntrackedHead(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "fixupauto", "FixupAuto Commit", 1)
	runGitSD(t, repoDir, "assign", "fixupauto:0")
	runGitSD(t, repoDir, "sync")

	// The fix commit is created without any 'track'/'status' call touching
	// it, so it has no git-sd-id when 'fixup' runs.
	filename := filepath.Join(repoDir, "fixupauto-fix.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "fix %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "FixupAuto Fix")

	runGitSD(t, repoDir, "fixup", "0")

	out := runGitSD(t, repoDir, "status")
	var fixLine string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "FixupAuto Fix") {
			fixLine = line
		}
	}
	if fixLine == "" {
		t.Fatalf("could not find the fixup commit's row in status:\n%s", out)
	}
	if strings.Contains(fixLine, "—") {
		t.Fatalf("expected fixup to auto-track HEAD (no '—' Untracked marker), got line: %s", fixLine)
	}
	if !strings.Contains(fixLine, "0.1") || !strings.Contains(fixLine, "fixupauto") {
		t.Fatalf("expected the auto-tracked commit to show as sub-commit 0.1 under fixupauto, got line: %s", fixLine)
	}
	fmt.Println("fixup verified: auto-tracks an untracked HEAD commit, no separate 'track' step needed.")
}

// TestSDFixupComputesNextSubIndexWhenParentAlreadyHasOne verifies the
// auto-rank algorithm advances past an existing sibling rather than
// clobbering it.
func TestSDFixupComputesNextSubIndexWhenParentAlreadyHasOne(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "fixupseq", "FixupSeq Commit", 1)
	runGitSD(t, repoDir, "assign", "fixupseq:0")

	for i, msg := range []string{"FixupSeq Fix One", "FixupSeq Fix Two"} {
		filename := filepath.Join(repoDir, fmt.Sprintf("fixupseq-fix%d.txt", i))
		if err := os.WriteFile(filename, fmt.Appendf(nil, "%s %d", msg, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", msg)
		runGitSD(t, repoDir, "fixup", "0")
	}

	out := runGitSD(t, repoDir, "status")
	var line1, line2 string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "FixupSeq Fix One") {
			line1 = line
		}
		if strings.Contains(line, "FixupSeq Fix Two") {
			line2 = line
		}
	}
	if !strings.Contains(line1, "0.1") {
		t.Fatalf("expected first fixup to be 0.1, got line: %s", line1)
	}
	if !strings.Contains(line2, "0.2") {
		t.Fatalf("expected second fixup to be 0.2 (not clobbering 0.1), got line: %s", line2)
	}
	fmt.Println("fixup rank sequencing verified: 0.1 then 0.2.")
}

// TestSDFixupRejectsUnassignedTarget verifies fixup refuses to attach to a
// target that isn't assigned to any Stack Set (nothing to inherit).
func TestSDFixupRejectsUnassignedTarget(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "fixupnoset", "FixupNoSet Commit", 2)
	runGitSD(t, repoDir, "track")

	headBefore := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	out, err := runGitSDErr(repoDir, "fixup", "0")
	if err == nil {
		t.Fatalf("expected fixup against an unassigned target to fail, got success. Output:\n%s", out)
	}
	if !strings.Contains(out, "Stack Set") {
		t.Fatalf("expected an unassigned-target error, got:\n%s", out)
	}
	if headAfter := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("rejected fixup rewrote the branch: HEAD %s -> %s", headBefore, headAfter)
	}
	fmt.Println("fixup correctly rejected an unassigned target; repo untouched.")
}

// TestSDMoveRejectsAttachingACommitThatHasItsOwnSubCommits verifies the
// shared attach validation (used by both 'move' and 'fixup') refuses to
// turn a commit that already has its own sub-commit(s) into someone else's
// sub-commit, which would orphan its children (creating hidden 3-level
// nesting). Note this specific precondition is unreachable via 'fixup'
// itself: a child always attaches right after its parent, and a plain
// 'move' of a with-children commit now carries its children along (see the
// "must move together" fix), so a parent with children can never end up
// alone at HEAD. It's directly reachable via 'move's ranked-attach form,
// though, which is what this test exercises.
func TestSDMoveRejectsAttachingACommitThatHasItsOwnSubCommits(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "orphanguard", "OrphanGuard Commit", 3)
	// 0: parent-with-a-child, 1: its child, 2: some other assigned target.
	runGitSD(t, repoDir, "assign", "orphanguard:0,2")
	runGitSD(t, repoDir, "move", "1:0.1") // commit 1 becomes sub-commit "0.1"

	headBefore := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	out, err := runGitSDErr(repoDir, "move", "0:2.5") // try to attach commit 0 (has a child) under commit 2
	if err == nil {
		t.Fatalf("expected move to reject attaching a commit that already has its own sub-commit, got success. Output:\n%s", out)
	}
	if !strings.Contains(out, "sub-commit") {
		t.Fatalf("expected an orphaning-sub-commits error, got:\n%s", out)
	}
	if headAfter := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("rejected move rewrote the branch: HEAD %s -> %s", headBefore, headAfter)
	}
	fmt.Println("move correctly refused to attach a commit that already has its own sub-commit(s) elsewhere.")
}

// TestSDMoveKeepsSubCommitsAttachedWhenParentIsRelocated verifies that
// moving a top-level commit with existing sub-commits carries the whole
// group along together, and that the sub-commits' displayed labels update
// to the parent's new index automatically (their rank digits are unchanged
// -- only the parent-index part of the label moves, since it's derived from
// the parent's current physical position, not stored).
func TestSDMoveKeepsSubCommitsAttachedWhenParentIsRelocated(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "groupmove", "GroupMove Commit", 3)
	// 0: parent, 1 & 2: two more commits, one of which becomes a sub-commit,
	// the other an unrelated top-level commit to move past.
	runGitSD(t, repoDir, "assign", "groupmove:0,2")
	runGitSD(t, repoDir, "move", "1:0.1") // commit 1 becomes sub-commit "0.1"

	out := runGitSD(t, repoDir, "status")
	var subLineBefore string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "GroupMove Commit 2") {
			subLineBefore = line
		}
	}
	if !strings.Contains(subLineBefore, "0.1") {
		t.Fatalf("expected sub-commit to show as 0.1 before the move, got line: %s", subLineBefore)
	}

	// Relocate the parent (with its child) past the other top-level commit.
	runGitSD(t, repoDir, "move", "0:2")

	out = runGitSD(t, repoDir, "status")
	var parentLine, subLineAfter string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "GroupMove Commit 1") {
			parentLine = line
		}
		if strings.Contains(line, "GroupMove Commit 2") {
			subLineAfter = line
		}
	}
	newParentIdx := ""
	if parentLine != "" {
		newParentIdx = statusIndex(t, out, parentLine)
	}
	if newParentIdx == "" {
		t.Fatalf("could not find parent's row after move. Status:\n%s", out)
	}
	if !strings.Contains(subLineAfter, newParentIdx+".1") {
		t.Fatalf("expected sub-commit's label to follow its relocated parent (index %s), got line: %s", newParentIdx, subLineAfter)
	}
	fmt.Println("move correctly relocated a parent together with its sub-commit; label followed automatically.")
}

// TestSDMoveRejectsThreeLevelNesting verifies the model stays exactly two
// levels: attaching a sub-commit under a commit that is ITSELF a
// sub-commit is refused.
func TestSDMoveRejectsThreeLevelNesting(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "nest3", "Nest3 Commit", 1)
	runGitSD(t, repoDir, "assign", "nest3:0")

	filename := filepath.Join(repoDir, "nest3-sub.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "sub %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "Nest3 Sub")
	runGitSD(t, repoDir, "fixup", "0") // commit at (old) HEAD becomes sub-commit "0.1"

	filename2 := filepath.Join(repoDir, "nest3-subsub.txt")
	if err := os.WriteFile(filename2, fmt.Appendf(nil, "subsub %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename2)
	runGit(t, repoDir, "commit", "-m", "Nest3 SubSub")
	runGitSD(t, repoDir, "track")

	headBefore := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	// Index 1 is now the sub-commit "0.1" itself -- attaching under it must
	// be refused (no 3-level nesting).
	out, err := runGitSDErr(repoDir, "move", "2:1.5")
	if err == nil {
		t.Fatalf("expected move to reject attaching under a sub-commit (3-level nesting), got success. Output:\n%s", out)
	}
	if headAfter := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("rejected move rewrote the branch: HEAD %s -> %s", headBefore, headAfter)
	}
	fmt.Println("move correctly rejected 3-level nesting; repo untouched.")
}

// TestSDAssignCascadesToSubCommits verifies that assigning/unassigning a
// top-level commit re-stamps every existing sub-commit's Stack Set too.
func TestSDAssignCascadesToSubCommits(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "cascade", "Cascade Commit", 1)
	runGitSD(t, repoDir, "assign", "cascade-a:0")

	filename := filepath.Join(repoDir, "cascade-sub.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "sub %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "Cascade Sub")
	runGitSD(t, repoDir, "fixup", "0")

	// Reassign the top-level commit to a different set -- the sub-commit
	// must follow automatically.
	runGitSD(t, repoDir, "assign", "cascade-b:0")
	out := runGitSD(t, repoDir, "status")
	var subLine string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Cascade Sub") {
			subLine = line
		}
	}
	if !strings.Contains(subLine, "cascade-b") || strings.Contains(subLine, "cascade-a") {
		t.Fatalf("expected sub-commit to cascade to cascade-b on reassign, got line: %s", subLine)
	}

	// Unassigning the top-level commit must cascade to "" too.
	runGitSD(t, repoDir, "assign", ":0")
	out = runGitSD(t, repoDir, "status")
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Cascade Sub") {
			subLine = line
		}
	}
	if strings.Contains(subLine, "cascade-b") || strings.Contains(subLine, "📦") {
		t.Fatalf("expected sub-commit to cascade to unassigned, got line: %s", subLine)
	}
	fmt.Println("assign cascade verified: reassign and unassign both propagate to sub-commits.")
}

// TestSDAssignRejectsHierarchicalAddress verifies assign's dotted-address
// forms: it never creates, attaches, or reparents a sub-commit -- a real
// Stack Set name is rejected unconditionally (never a no-op), while
// unassigning (":N.R") still works as a one-step full detach.
func TestSDAssignRejectsHierarchicalAddress(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "hiercheck", "HierCheck Commit", 1)
	runGitSD(t, repoDir, "assign", "hiercheck:0")

	filename := filepath.Join(repoDir, "hiercheck-sub.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "sub %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "HierCheck Sub")
	runGitSD(t, repoDir, "fixup", "0") // creates sub-commit "0.1"

	// Same set as its parent: still rejected -- assign never changes a
	// sub-commit's state via a dotted address with a real set name, even
	// when that address already matches (no no-op case).
	headBefore := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	out, err := runGitSDErr(repoDir, "assign", "hiercheck:0.1")
	if err == nil {
		t.Fatalf("expected assign with a matching set name to still be rejected, got success. Output:\n%s", out)
	}
	if !strings.Contains(out, "move") {
		t.Fatalf("expected the error to direct the user to 'move', got:\n%s", out)
	}

	// Different set: also rejected, directing to 'move'.
	rout, rerr := runGitSDErr(repoDir, "assign", "othertset:0.1")
	if rerr == nil {
		t.Fatalf("expected assigning a sub-commit to a different set to fail, got success. Output:\n%s", rout)
	}
	if !strings.Contains(rout, "move") {
		t.Fatalf("expected the error to direct the user to 'move', got:\n%s", rout)
	}
	if headAfter := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("rejected assign rewrote the branch: HEAD %s -> %s", headBefore, headAfter)
	}

	// Unassigning via a dotted address is the one form that still works --
	// a real, one-step action (full detach), not a no-op-or-error.
	uout := runGitSD(t, repoDir, "assign", ":0.1")
	if !strings.Contains(uout, "successfully") {
		t.Fatalf("expected unassign via dotted address to succeed, got: %s", uout)
	}
	statusOut := runGitSD(t, repoDir, "status")
	var subLine string
	for line := range strings.SplitSeq(statusOut, "\n") {
		if strings.Contains(line, "HierCheck Sub") {
			subLine = line
		}
	}
	if strings.Contains(subLine, "hiercheck") || strings.Contains(subLine, "📦") || strings.Contains(subLine, "0.1") {
		t.Fatalf("expected unassign to fully detach the sub-commit, got line: %s", subLine)
	}
	fmt.Println("assign dotted-address behavior verified: rejected with a real set name (match or not), still works for unassign/detach.")
}
