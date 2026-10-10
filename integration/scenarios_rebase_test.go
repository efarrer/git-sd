package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mergeViaGitHub merges a PR directly through GitHub (not `git sd merge`),
// the way a user clicking "Merge" in the UI would, with the given method
// ("squash", "merge", or "rebase"). A PR in a registered GitHub Stack can
// only be merged through the async merge REST API (GitHub refuses
// `gh pr merge` for it), so that's used here; right after a push GitHub's
// mergeability is briefly unknown (B-014), so submission is retried, then
// the PR is polled until it reports MERGED.
func mergeViaGitHub(t *testing.T, dir, prNumber, method string) {
	t.Helper()
	var lastErr error
	submitted := false
	for range 12 {
		cmd := exec.Command("gh", "api", "-X", "PUT", "repos/{owner}/{repo}/pulls/"+prNumber+"/merge-async", "-f", "merge_method="+method)
		cmd.Dir = dir
		cmd.Env = ghEnv(authorToken)
		out, err := cmd.CombinedOutput()
		if err == nil {
			submitted = true
			break
		}
		lastErr = fmt.Errorf("%w: %s", err, out)
		time.Sleep(5 * time.Second)
	}
	if !submitted {
		t.Fatalf("could not submit PR #%s for a %s merge: %v", prNumber, method, lastErr)
	}
	for range 60 {
		if prState(t, dir, prNumber) == "MERGED" {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("PR #%s never reached MERGED after submitting a %s merge", prNumber, method)
}

func remoteBranchExists(t *testing.T, dir, branch string) bool {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "ls-remote", "--heads", "origin", branch)) != ""
}

func metadataBytes(t *testing.T, dir string) []byte {
	t.Helper()
	gitDir := strings.TrimSpace(runGit(t, dir, "rev-parse", "--absolute-git-dir"))
	data, err := os.ReadFile(filepath.Join(gitDir, "sd", "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	return data
}

// TestSDRebaseDropsPRMergedViaGitHubForEachMergeShape covers the case the
// rebase command exists for: a PR merged outside `git sd merge`, with
// whichever merge method GitHub was told to use. The bottom PR carries a
// sub-commit (a 2-commit group), so the rebase-merge shape exercises the
// mergeCommit~N candidate rather than collapsing to the squash case.
func TestSDRebaseDropsPRMergedViaGitHubForEachMergeShape(t *testing.T) {
	t.Parallel()
	for _, shape := range []struct{ name, set, flag string }{
		{"Squash", "rbsquash", "squash"},
		{"MergeCommit", "rbmerge", "merge"},
		{"RebaseMerge", "rbrebase", "rebase"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			dir := newTestRepo(t)
			fmt.Printf("--- Running Scenario: Rebase after GitHub %s merge ---\n", shape.name)

			makeCommits(t, dir, shape.set, shape.set+" Commit", 2)
			runGitSD(t, dir, "assign", shape.set+":0-1")
			fix := filepath.Join(dir, shape.set+"-fix.txt")
			if err := os.WriteFile(fix, fmt.Appendf(nil, "fix %d", os.Getpid()), 0644); err != nil {
				t.Fatal(err)
			}
			runGit(t, dir, "add", fix)
			runGit(t, dir, "commit", "-m", shape.set+" Fix")
			runGitSD(t, dir, "fixup", "0")
			runGitSD(t, dir, "sync", shape.set)

			out := runGitSD(t, dir, "status")
			prs := extractStackPRNumbers(out, shape.set)
			if len(prs) != 2 {
				t.Fatalf("expected 2 PRs, got %v. Status:\n%s", prs, out)
			}
			bottomBranch := prHeadRef(t, dir, prs[0])

			mergeViaGitHub(t, dir, prs[0], shape.flag)

			out = runGitSD(t, dir, "rebase")
			fmt.Println(out)
			if !strings.Contains(out, "dropped 1 landed PR(s)") {
				t.Fatalf("expected rebase to drop the merged PR. Output:\n%s", out)
			}
			localLog := runGit(t, dir, "log", "--format=%s", "origin/main..HEAD")
			if strings.Contains(localLog, shape.set+" Commit 1") || strings.Contains(localLog, shape.set+" Fix") {
				t.Fatalf("merged commits still on the local branch:\n%s", localLog)
			}
			if !strings.Contains(localLog, shape.set+" Commit 2") {
				t.Fatalf("surviving commit missing from the local branch:\n%s", localLog)
			}
			out = runGitSD(t, dir, "status")
			if survivors := extractStackPRNumbers(out, shape.set); len(survivors) != 1 || survivors[0] != prs[1] {
				t.Fatalf("expected only PR #%s still tracked, got %v. Status:\n%s", prs[1], survivors, out)
			}
			if !remoteBranchExists(t, dir, bottomBranch) {
				t.Fatalf("rebase must be local-only, but merged branch %s was deleted from the remote", bottomBranch)
			}

			out = runGitSD(t, dir, "sync", shape.set)
			fmt.Println(out)
			if remoteBranchExists(t, dir, bottomBranch) {
				t.Fatalf("sync did not delete the merged PR's branch %s. Output:\n%s", bottomBranch, out)
			}
			top := prInfo(t, dir, prs[1])
			if top.BaseRefName != "main" {
				t.Fatalf("surviving PR #%s should base on main after sync, got %q", prs[1], top.BaseRefName)
			}
			runGit(t, dir, "fetch", "origin")
			topLog := runGit(t, dir, "log", "--format=%s", "origin/main..origin/"+top.HeadRefName)
			if strings.TrimSpace(topLog) != shape.set+" Commit 2" {
				t.Fatalf("surviving PR's branch should carry only its own commit, got:\n%s", topLog)
			}
			fmt.Printf("Rebase verified for %s merges.\n", shape.name)
		})
	}
}

// TestSDRebaseMergedContentConflictsAreResolvedWithGit: the local commit is
// amended after its last sync (editing the very file that landed), then the
// older PR is merged. The local changes collide with what landed, so rebase
// leaves a standard git rebase in progress on a follow-up commit: --abort
// restores everything, and resolving + --continue keeps the follow-up
// (assigned to the same Stack Set, no PR) for a later sync to open.
func TestSDRebaseMergedContentConflictsAreResolvedWithGit(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	makeCommits(t, dir, "rbmism", "RbMism Commit", 1)
	runGitSD(t, dir, "assign", "rbmism:0")
	runGitSD(t, dir, "sync", "rbmism")
	prs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbmism")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %v", prs)
	}

	if err := os.WriteFile(filepath.Join(dir, "rbmism1.txt"), []byte("edited locally, never synced\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-a", "--amend", "--no-edit")
	mergeViaGitHub(t, dir, prs[0], "squash")

	headBefore := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	out, err := runGitSDErr(dir, "rebase")
	if err == nil {
		t.Fatalf("expected rebase to stop on a conflict. Output:\n%s", out)
	}
	for _, want := range []string{"Conflict while rebasing", "Follow-up to PR #" + prs[0], "rbmism1.txt", "git rebase --abort"} {
		if !strings.Contains(out, want) {
			t.Fatalf("conflict output missing %q:\n%s", want, out)
		}
	}
	if st := runGit(t, dir, "status"); !strings.Contains(st, "rebase in progress") {
		t.Fatalf("expected a git rebase in progress, got:\n%s", st)
	}

	// --abort restores the branch exactly and leaves no backup behind.
	runGit(t, dir, "rebase", "--abort")
	if got := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("HEAD after abort = %s, want %s", got, headBefore)
	}
	if out := runGitSD(t, dir, "rebase", "--list"); !strings.Contains(out, "No rebase backups") {
		t.Fatalf("abort should discard the backup, got:\n%s", out)
	}
	if st := runGitSD(t, dir, "status"); !strings.Contains(st, "RbMism Commit 1") || strings.Contains(st, "Follow-up") {
		t.Fatalf("after abort the original commit must be unchanged and no follow-up present:\n%s", st)
	}

	// Run again, resolve, continue: the follow-up is kept under the same set.
	if _, err := runGitSDErr(dir, "rebase"); err == nil {
		t.Fatal("expected the second rebase to conflict as well")
	}
	if err := os.WriteFile(filepath.Join(dir, "rbmism1.txt"), []byte("resolved by hand\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "rbmism1.txt")
	runGit(t, dir, "-c", "core.editor=true", "rebase", "--continue")
	st := runGitSD(t, dir, "status")
	// The follow-up's subject contains the original subject, so count rows:
	// exactly one commit should remain, and it must be the follow-up.
	if strings.Count(st, "RbMism Commit 1") != 1 || !strings.Contains(st, "Follow-up to PR #"+prs[0]+": RbMism Commit 1") {
		t.Fatalf("after --continue status should show only the follow-up commit:\n%s", st)
	}
	runGitSD(t, dir, "sync", "rbmism")
	newPRs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbmism")
	if len(newPRs) != 1 || newPRs[0] == prs[0] {
		t.Fatalf("expected sync to open a NEW PR for the follow-up (old was #%s), got %v", prs[0], newPRs)
	}
	fmt.Println("Rebase conflict on merged content verified: abort restores, continue keeps a follow-up that syncs to a new PR.")
}

// TestSDRebaseKeepsLocalOnlyChangesAsFollowUp: the local commit is amended
// after its last sync with an EXTRA file, then the older PR is merged. The
// extra file never landed, so rebase keeps just that as one follow-up commit
// (no stop), assigned to the same Stack Set; sync then opens a PR carrying
// only the extra file.
func TestSDRebaseKeepsLocalOnlyChangesAsFollowUp(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	makeCommits(t, dir, "rbfu", "RbFu Commit", 1)
	runGitSD(t, dir, "assign", "rbfu:0")
	runGitSD(t, dir, "sync", "rbfu")
	prs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbfu")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %v", prs)
	}

	if err := os.WriteFile(filepath.Join(dir, "rbfuextra.txt"), []byte("added after the last sync\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "rbfuextra.txt")
	runGit(t, dir, "commit", "--amend", "--no-edit")
	mergeViaGitHub(t, dir, prs[0], "squash")

	out := runGitSD(t, dir, "rebase")
	fmt.Println(out)
	for _, want := range []string{"Kept local changes from PR #" + prs[0] + " as a new commit (assigned to 'rbfu')", "dropped 1 landed PR(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rebase output missing %q:\n%s", want, out)
		}
	}
	if st := runGit(t, dir, "status"); strings.Contains(st, "rebase in progress") {
		t.Fatalf("a clean delta must not stop the rebase:\n%s", st)
	}
	st := runGitSD(t, dir, "status")
	if !strings.Contains(st, "Follow-up to PR #"+prs[0]) || !strings.Contains(st, "rbfu") {
		t.Fatalf("status should show the follow-up commit in the same set:\n%s", st)
	}

	runGitSD(t, dir, "sync", "rbfu")
	newPRs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbfu")
	if len(newPRs) != 1 || newPRs[0] == prs[0] {
		t.Fatalf("expected sync to open a NEW PR for the follow-up (old was #%s), got %v", prs[0], newPRs)
	}
	files := strings.TrimSpace(runGit(t, dir, "pr", "diff", newPRs[0], "--name-only"))
	if files != "rbfuextra.txt" {
		t.Fatalf("the follow-up PR should carry only the extra file, got:\n%s", files)
	}
	fmt.Println("Follow-up verified: only the un-landed change was kept, and sync opened a new PR for it.")
}

// TestSDRebaseRollbackRestoresBranchAndMetadata: rolling back a successful
// rebase brings back both the dropped commits and their tracking, leaving
// state consistent enough that rebasing again works.
func TestSDRebaseRollbackRestoresBranchAndMetadata(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	makeCommits(t, dir, "rbroll", "RbRoll Commit", 1)
	runGitSD(t, dir, "assign", "rbroll:0")
	runGitSD(t, dir, "sync", "rbroll")
	prs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbroll")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %v", prs)
	}
	mergeViaGitHub(t, dir, prs[0], "squash")

	headBefore := runGit(t, dir, "rev-parse", "HEAD")
	metaBefore := metadataBytes(t, dir)
	if out := runGitSD(t, dir, "rebase"); !strings.Contains(out, "dropped 1 landed PR(s)") {
		t.Fatalf("expected rebase to drop the merged PR. Output:\n%s", out)
	}
	if out := runGitSD(t, dir, "rebase", "--list"); !strings.HasPrefix(out, "1  ") {
		t.Fatalf("expected --list to show backup #1. Output:\n%s", out)
	}

	out := runGitSD(t, dir, "rebase", "--rollback")
	if !strings.Contains(out, "Rolled back to backup #1") {
		t.Fatalf("unexpected rollback output:\n%s", out)
	}
	if headAfter := runGit(t, dir, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("rollback left HEAD at %s, want %s", headAfter, headBefore)
	}
	if string(metadataBytes(t, dir)) != string(metaBefore) {
		t.Fatalf("rollback did not restore metadata exactly.\nbefore: %s\nafter:  %s", metaBefore, metadataBytes(t, dir))
	}
	if out := runGitSD(t, dir, "rebase", "--list"); !strings.Contains(out, "No rebase backups.") {
		t.Fatalf("expected the consumed backup to be gone. Output:\n%s", out)
	}
	if out := runGitSD(t, dir, "rebase"); !strings.Contains(out, "dropped 1 landed PR(s)") {
		t.Fatalf("expected rebase to work again after rollback. Output:\n%s", out)
	}
	fmt.Println("Rebase rollback verified.")
}

// TestSDSyncKeepsFinishedBranchWhileAnOpenPRIsBasedOnIt: deleting a PR's
// base branch makes GitHub close that PR, so sync must not delete a retired
// branch that an open PR still targets. The dependent PR here is created
// outside git-sd (a closed PR's branch is never retargeted by GitHub, and
// sync doesn't know about foreign PRs), which is exactly the case sync's
// own retargeting can't cover. Once it's gone, the next sync deletes the
// still-queued branch.
func TestSDSyncKeepsFinishedBranchWhileAnOpenPRIsBasedOnIt(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	makeCommits(t, dir, "rbguard", "RbGuard Commit", 1)
	runGitSD(t, dir, "assign", "rbguard:0")
	runGitSD(t, dir, "sync", "rbguard")
	prs := extractStackPRNumbers(runGitSD(t, dir, "status"), "rbguard")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %v", prs)
	}
	finished := prHeadRef(t, dir, prs[0])

	// A foreign PR based on git-sd's branch. Its name matches git-sd's branch
	// pattern so sanitizeRepo sweeps it if this test dies midway.
	foreign := fmt.Sprintf("rbguard-foreign-s%04d", os.Getpid()%10000)
	runGit(t, dir, "fetch", "origin", finished)
	runGit(t, dir, "checkout", "-q", "-b", foreign, "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(dir, "rbguard-foreign.txt"), fmt.Appendf(nil, "foreign %d", os.Getpid()), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "rbguard-foreign.txt")
	runGit(t, dir, "commit", "-q", "-m", "RbGuard foreign work")
	runGit(t, dir, "push", "-q", "origin", foreign)
	prURL := strings.TrimSpace(runGit(t, dir, "pr", "create", "--base", finished, "--head", foreign, "--title", "RbGuard foreign PR", "--body", "Based on a git-sd branch."))
	foreignPR := prURL[strings.LastIndex(prURL, "/")+1:]
	runGit(t, dir, "checkout", "-q", "-")

	runGit(t, dir, "pr", "close", prs[0])
	runGitSD(t, dir, "rebase")

	out := runGitSD(t, dir, "sync", "rbguard")
	fmt.Println(out)
	if !strings.Contains(out, "keeping finished branch "+finished) || !strings.Contains(out, "#"+foreignPR) {
		t.Fatalf("expected sync to keep %s because PR #%s is based on it. Output:\n%s", finished, foreignPR, out)
	}
	if !remoteBranchExists(t, dir, finished) {
		t.Fatalf("sync deleted %s even though PR #%s is based on it", finished, foreignPR)
	}
	if state := prState(t, dir, foreignPR); state != "OPEN" {
		t.Fatalf("dependent PR #%s should still be OPEN, got %q", foreignPR, state)
	}

	runGit(t, dir, "pr", "close", foreignPR, "--delete-branch")
	out = runGitSD(t, dir, "sync", "rbguard")
	fmt.Println(out)
	if remoteBranchExists(t, dir, finished) {
		t.Fatalf("once nothing was based on it, sync should have deleted the still-queued %s. Output:\n%s", finished, out)
	}
	fmt.Println("Dependent-PR guard verified: branch kept while targeted, deleted once free.")
}

// TestSDRebaseConflictLeavesStandardRebase provokes a real upstream conflict
// (on a throwaway base branch, so parallel tests' main is untouched) and
// checks the user can get out with plain git either way: --abort restores
// the branch exactly, and a resolved --continue leaves the rebased branch
// with a kept, usable backup.
func TestSDRebaseConflictLeavesStandardRebase(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Rebase Conflict Leaves A Standard Rebase ---")
	baseBranch := "sd-test-rebase-conflict-base"

	runGitErr(repoDir, "push", "origin", "--delete", baseBranch)
	runGitErr(repoDir, "branch", "-D", baseBranch)
	defer func() {
		runGitErr(repoDir, "rebase", "--abort")
		runGitErr(repoDir, "checkout", "-f", "main")
		runGitErr(repoDir, "branch", "-D", baseBranch)
		runGitErr(repoDir, "branch", "-D", "rbconflict-work")
		runGitErr(repoDir, "push", "origin", "--delete", baseBranch)
	}()

	// The base branch starts with a 3-line file; local work edits line 2 and
	// adds an unrelated file; then upstream edits the same line 2 differently.
	runGit(t, repoDir, "checkout", "-b", baseBranch)
	writeFile(t, repoDir, "rbconflict.txt", "line1\nline2\nline3\n")
	runGit(t, repoDir, "add", "rbconflict.txt")
	runGit(t, repoDir, "commit", "-m", "Base file")
	runGit(t, repoDir, "push", "-u", "origin", baseBranch)

	runGit(t, repoDir, "checkout", "-b", "rbconflict-work")
	writeFile(t, repoDir, "rbconflict.txt", "line1\nLOCAL\nline3\n")
	runGit(t, repoDir, "commit", "-am", "Local edit")
	writeFile(t, repoDir, "rbclean.txt", "clean\n")
	runGit(t, repoDir, "add", "rbclean.txt")
	runGit(t, repoDir, "commit", "-m", "Clean follow-up")
	origHead := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	runGit(t, repoDir, "checkout", baseBranch)
	writeFile(t, repoDir, "rbconflict.txt", "line1\nUPSTREAM\nline3\n")
	runGit(t, repoDir, "commit", "-am", "Upstream edit")
	runGit(t, repoDir, "push", "origin", baseBranch)
	runGit(t, repoDir, "checkout", "rbconflict-work")
	runGit(t, repoDir, "config", "sd.base-branch", "origin/"+baseBranch)
	runGitSD(t, repoDir, "track")
	origHead = strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")) // track re-tagged the commits

	// 1. The conflict leaves a standard rebase in progress with a helpful message.
	out, err := runGitSDErr(repoDir, "rebase")
	if err == nil {
		t.Fatalf("expected git sd rebase to stop on the conflict, got success:\n%s", out)
	}
	for _, want := range []string{"Conflict while rebasing", "rbconflict.txt", "git rebase --continue", "git rebase --abort"} {
		if !strings.Contains(out, want) {
			t.Fatalf("conflict output missing %q:\n%s", want, out)
		}
	}
	if st := runGit(t, repoDir, "status"); !strings.Contains(st, "rebase in progress") {
		t.Fatalf("expected a git rebase in progress, got:\n%s", st)
	}
	// 2. While it is in progress, state-changing commands refuse; status notes it.
	if out, err := runGitSDErr(repoDir, "assign", "x:0"); err == nil || !strings.Contains(out, "git rebase --continue") {
		t.Fatalf("expected assign to refuse during an in-progress rebase, got err=%v out=%s", err, out)
	}

	// 3. --abort restores the branch exactly and leaves no backup behind.
	runGit(t, repoDir, "rebase", "--abort")
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); got != origHead {
		t.Fatalf("HEAD after abort = %s, want %s", got, origHead)
	}
	if out := runGitSD(t, repoDir, "rebase", "--list"); !strings.Contains(out, "No rebase backups") {
		t.Fatalf("abort should discard the backup, got:\n%s", out)
	}

	// 4. Run it again, resolve, and --continue: both commits end up on the new
	// base, and the backup is kept for --rollback.
	if _, err := runGitSDErr(repoDir, "rebase"); err == nil {
		t.Fatal("expected the second rebase to conflict as well")
	}
	writeFile(t, repoDir, "rbconflict.txt", "line1\nRESOLVED\nline3\n")
	runGit(t, repoDir, "add", "rbconflict.txt")
	runGit(t, repoDir, "-c", "core.editor=true", "rebase", "--continue")
	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "Local edit") || !strings.Contains(out, "Clean follow-up") {
		t.Fatalf("status after --continue should list both commits, got:\n%s", out)
	}
	if out := runGitSD(t, repoDir, "rebase", "--list"); strings.Contains(out, "No rebase backups") {
		t.Fatalf("--continue should keep the backup, got:\n%s", out)
	}
	runGitSD(t, repoDir, "rebase", "--rollback")
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); got != origHead {
		t.Fatalf("HEAD after --rollback = %s, want %s", got, origHead)
	}
	fmt.Println("Rebase conflict verified: abort and continue both leave a consistent state.")
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("%v", err)
	}
}
