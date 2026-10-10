package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDHappyPathWithinAGitWorktree(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Worktree Metadata
	//
	// Regression guard: git-sd used to hardcode ".git/sd/metadata.json" as a
	// relative path, which fails with ENOTDIR inside a linked worktree (where
	// ".git" is a file, not a directory, e.g. "gitdir: .../worktrees/<name>").
	// Confirm every metadata-touching command (status/assign/move/sync/merge/
	// rebase) resolves the real per-worktree git dir and works from a
	// worktree, with metadata isolated to that worktree.
	fmt.Println("--- Running Scenario: Worktree Metadata ---")
	wtDir := filepath.Join(os.TempDir(), fmt.Sprintf("git-sd-worktree-%d", os.Getpid()))
	os.RemoveAll(wtDir) // defensive: clear a leftover from a crashed prior run
	runGit(t, repoDir, "worktree", "add", wtDir, "-b", "sd-test-worktree", "origin/main")

	// Teardown regardless of outcome. sd-test-worktree is never pushed to the
	// remote (only sd/-prefixed stack branches are), so there's nothing for
	// sanitizeRepo to sweep — local cleanup is sufficient.
	defer func() {
		if _, rerr := runGitErr(repoDir, "worktree", "remove", "--force", wtDir); rerr != nil {
			t.Errorf("failed to remove worktree: %v", rerr)
		}
		if _, berr := runGitErr(repoDir, "branch", "-D", "sd-test-worktree"); berr != nil {
			t.Errorf("failed to delete worktree branch: %v", berr)
		}
		os.RemoveAll(wtDir)
	}()

	runWorktreeChecks(t, repoDir, wtDir)
	fmt.Println("Worktree metadata scenario verified.")
}

// runWorktreeChecks exercises every metadata-touching git-sd command from
// inside a linked worktree (whose ".git" is a file, not a directory), proving
// the metadata path resolves the real per-worktree git dir rather than the old
// hardcoded relative ".git/sd/metadata.json". wtDir is an already-created
// worktree checked out on a fresh branch at origin/main; the caller owns its
// teardown.
func runWorktreeChecks(t *testing.T, repoDir, wtDir string) {
	t.Helper()
	// Core regression check: the exact user repro — `git sd status` in a
	// worktree — must succeed instead of failing with the ENOTDIR metadata
	// error. (No commits ahead of origin/main yet, so it reports the empty
	// state; the point is that it exits 0 and reaches metadata at all.)
	runGitSD(t, wtDir, "status")
	fmt.Println("Worktree metadata verified: git sd status runs in a linked worktree without ENOTDIR.")

	// move: needs >=2 commits to reorder. Use independent files so the
	// cherry-pick reorder can't conflict. This confirms move reads/writes
	// metadata from a worktree too.
	for _, mc := range []struct{ file, msg string }{
		{"wt-move-a.txt", "WT Move A"},
		{"wt-move-b.txt", "WT Move B"},
	} {
		if err := os.WriteFile(filepath.Join(wtDir, mc.file), []byte(mc.msg), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, wtDir, "add", mc.file)
		runGit(t, wtDir, "commit", "-m", mc.msg)
	}
	idxOf := func(statusOut, msg string) string {
		for line := range strings.SplitSeq(statusOut, "\n") {
			if strings.Contains(line, msg) {
				return statusIndex(t, statusOut, line)
			}
		}
		return ""
	}
	runGitSD(t, wtDir, "track") // assigns git-sd-ids
	out := runGitSD(t, wtDir, "status")
	srcIdx := idxOf(out, "WT Move B")
	if srcIdx == "" {
		t.Fatalf("could not find 'WT Move B' index in worktree status:\n%s", out)
	}
	runGitSD(t, wtDir, "move", fmt.Sprintf("%s:0", srcIdx))
	out = runGitSD(t, wtDir, "status")
	if got := idxOf(out, "WT Move B"); got != "0" {
		t.Fatalf("git sd move in worktree did not reorder: expected 'WT Move B' at index 0, got %q. Status:\n%s", got, out)
	}
	fmt.Println("Worktree move verified: git sd move reorders commits from a linked worktree.")

	// Reset the worktree branch so the full-cycle helper below starts from a
	// clean single-commit state.
	runGit(t, wtDir, "reset", "--hard", "origin/main")

	// Full read/write cycle from the worktree (status/assign/sync/merge/rebase),
	// reusing the shared helper by pointing it at the worktree as its working dir.
	runBaseBranchScenario(t, wtDir, baseBranchScenario{
		label:       "Worktree Metadata",
		stackSet:    "wtchk",
		fileName:    "worktree.txt",
		commitMsg:   "Worktree Commit",
		configValue: "",
		wantBaseRef: "main",
	})

	// Isolation: metadata must live in the worktree's own git dir, distinct
	// from the primary checkout's.
	wtGitDir := runGit(t, wtDir, "rev-parse", "--absolute-git-dir")
	mainGitDir := runGit(t, repoDir, "rev-parse", "--absolute-git-dir")
	if strings.TrimSpace(wtGitDir) == strings.TrimSpace(mainGitDir) {
		t.Fatalf("expected worktree git dir to differ from the primary checkout's, both were %q", strings.TrimSpace(wtGitDir))
	}
	wtMeta := filepath.Join(strings.TrimSpace(wtGitDir), "sd", "metadata.json")
	if _, err := os.Stat(wtMeta); err != nil {
		t.Fatalf("expected per-worktree metadata at %s, but it is not there: %v", wtMeta, err)
	}
	fmt.Printf("Worktree isolation verified: metadata stored at %s, separate from the primary checkout.\n", wtMeta)
}
