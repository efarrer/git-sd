package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestSDReportsHelpfulErrorWhenGhMissingOrUnauthenticated(t *testing.T) {
	t.Parallel()
	// This scenario never touches GitHub: it uses a local bare "origin", and a
	// PATH holding only git (gh missing) or git plus a fake gh that exits 4
	// (gh's "authentication required"). git-sd has no startup check, so local
	// commands work without gh, and a command that needs gh reports the same
	// message wherever it first notices.
	fmt.Println("--- Running Scenario 1: GitHub CLI Availability ---")
	dir := t.TempDir()
	remote := filepath.Join(dir, "origin.git")
	repo := filepath.Join(dir, "repo")
	gitIn := func(d string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}
	gitIn(dir, "init", "-q", "--bare", remote)
	gitIn(dir, "init", "-q", "-b", "main", repo)
	gitIn(repo, "config", "user.name", "t")
	gitIn(repo, "config", "user.email", "t@example.com")
	gitIn(repo, "remote", "add", "origin", remote)
	for _, c := range []struct{ file, msg string }{{"base.txt", "base"}} {
		if err := os.WriteFile(filepath.Join(repo, c.file), []byte(c.msg+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitIn(repo, "add", c.file)
		gitIn(repo, "commit", "-q", "-m", c.msg)
	}
	gitIn(repo, "push", "-q", "-u", "origin", "main")
	for _, c := range []struct{ file, msg string }{{"a.txt", "Commit A"}, {"b.txt", "Commit B"}} {
		if err := os.WriteFile(filepath.Join(repo, c.file), []byte(c.msg+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitIn(repo, "add", c.file)
		gitIn(repo, "commit", "-q", "-m", c.msg)
	}

	// Two PATHs: git only (gh missing), and git plus a gh that says "log in".
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	noGH := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(noGH, "git")); err != nil {
		t.Fatal(err)
	}
	loggedOut := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(loggedOut, "git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loggedOut, "gh"), []byte("#!/bin/sh\necho 'To get started with GitHub CLI, please run:  gh auth login' >&2\nexit 4\n"), 0755); err != nil {
		t.Fatal(err)
	}

	run := func(pathDir string, args ...string) (stdout, stderr string, err error) {
		t.Helper()
		cmd := exec.Command(gitSDPath, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "PATH="+pathDir)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err = cmd.Run()
		return o.String(), e.String(), err
	}
	pushedBranches := func() string {
		return strings.TrimSpace(gitIn(dir, "--git-dir", remote, "for-each-ref", "--format=%(refname:short)", "refs/heads"))
	}

	// Local commands need no gh at all.
	if _, e, err := run(noGH, "help"); err != nil {
		t.Fatalf("'help' should work without gh: %v\n%s", err, e)
	}
	if out, e, err := run(noGH, "track"); err != nil {
		t.Fatalf("'track' should work without gh: %v\n%s%s", err, out, e)
	}
	if out, e, err := run(noGH, "assign", "sa:0", "sb:1"); err != nil {
		t.Fatalf("'assign' should work without gh: %v\n%s%s", err, out, e)
	}
	// With no PRs to look up, status needs nothing from gh either.
	if out, e, err := run(noGH, "status"); err != nil || strings.Contains(e, "Warning") || !strings.Contains(out, "Commit A") {
		t.Fatalf("'status' with no PRs should work silently without gh: err=%v\nstdout:\n%s\nstderr:\n%s", err, out, e)
	}
	fmt.Println("Local commands verified: help, track, assign and PR-less status work without gh.")

	// Give commit B a PR number, as if it had been synced.
	gitDir := strings.TrimSpace(gitIn(repo, "rev-parse", "--absolute-git-dir"))
	metaPath := filepath.Join(gitDir, "sd", "metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	for _, v := range meta["commits"].(map[string]any) {
		cm := v.(map[string]any)
		if cm["stack_set"] == "sb" {
			cm["pr_number"] = 77
			cm["pr_url"] = "https://github.com/example/repo/pull/77"
		}
	}
	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, pathDir, want string
	}{
		{"gh missing", noGH, "'gh' CLI not found"},
		{"gh not authenticated", loggedOut, "not authenticated with 'gh'"},
	} {
		// status: still prints the table, then one warning, and exits 0.
		out, e, err := run(c.pathDir, "status")
		if err != nil {
			t.Fatalf("[%s] status should degrade, not fail: %v\nstdout:\n%s\nstderr:\n%s", c.name, err, out, e)
		}
		if !strings.Contains(out, "Commit B") || !strings.Contains(out, "pull/77") {
			t.Fatalf("[%s] status should still print the local table:\n%s", c.name, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "Commit B") && !strings.Contains(line, "\u2753") {
				t.Fatalf("[%s] the PR that could not be looked up should show the unavailable marker:\n%s", c.name, out)
			}
		}
		if !strings.Contains(e, "Warning: PR states unavailable") || !strings.Contains(e, c.want) || strings.Count(e, "Warning") != 1 {
			t.Fatalf("[%s] expected exactly one 'PR states unavailable' warning mentioning %q, got stderr:\n%s", c.name, c.want, e)
		}

		// sync: fails with the same message BEFORE pushing anything.
		out, e, err = run(c.pathDir, "sync", "sa")
		if err == nil {
			t.Fatalf("[%s] sync should fail:\n%s%s", c.name, out, e)
		}
		if !strings.Contains(out+e, c.want) {
			t.Fatalf("[%s] sync should report %q, got:\n%s%s", c.name, c.want, out, e)
		}
		if got := pushedBranches(); got != "main" {
			t.Fatalf("[%s] sync pushed branches before failing; remote has:\n%s", c.name, got)
		}
		fmt.Printf("GitHub CLI availability verified (%s): status warns once, sync fails before pushing.\n", c.name)
	}

	// A gh that fails for a reason that is NOT about authentication (a transient
	// GitHub error): the PR shows as unavailable, the PR-less commit is still
	// Assigned, and one warning names the PR and the error. status still exits 0.
	flaky := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(flaky, "git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flaky, "gh"), []byte("#!/bin/sh\necho 'GraphQL: Something went wrong while executing your query' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, e, err := run(flaky, "status")
	if err != nil {
		t.Fatalf("[flaky gh] status should degrade, not fail: %v\nstdout:\n%s\nstderr:\n%s", err, out, e)
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "Commit B") && !strings.Contains(line, "\u2753"):
			t.Fatalf("[flaky gh] the PR whose lookup failed should show the unavailable marker:\n%s", out)
		case strings.Contains(line, "Commit A") && !strings.Contains(line, "\U0001F4E6"):
			t.Fatalf("[flaky gh] the commit with no PR should stay Assigned:\n%s", out)
		}
	}
	if !strings.Contains(e, "Warning: could not read the state of PR #77:") || !strings.Contains(e, "Something went wrong") || strings.Contains(e, "PR states unavailable") {
		t.Fatalf("[flaky gh] expected one 'could not read the state of PR #77' warning (not the gh-environment one), got stderr:\n%s", e)
	}
	fmt.Println("Failed PR lookup verified: shown as unavailable with one warning, never as Assigned.")
}

// TestSDStackSetHappyPathFromCommitsThroughMerge runs the sequential feat-a
// chain (commit identification through rebase/sync teardown) in a single isolated clone.
// These steps deliberately share feat-a state and must run in order, so they
// stay grouped as ordered subtests of one parallel top-level test rather than
// becoming independent parallel tests.
func TestSDStackSetHappyPathFromCommitsThroughMerge(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	for _, step := range []struct {
		name string
		fn   func(*testing.T, string)
	}{
		{"CommitIdentification", stepCommitIdentification},
		{"StackSetAssignment", stepStackSetAssignment},
		{"Unassignment", stepUnassignment},
		{"ReorderingMove", stepReorderingMove},
		{"SyncingAndPRCreation", stepSyncingAndPRCreation},
		{"BranchNamingSlug", stepBranchNamingSlug},
		{"Merging", stepMerging},
		{"RebaseAndSync", stepRebaseAndSync},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.fn(t, repoDir)
		})
	}
}

func stepCommitIdentification(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 2: Commit Identification ---")

	// 1. Create a few commits
	for i := 1; i <= 3; i++ {
		filename := filepath.Join(repoDir, fmt.Sprintf("file%d.txt", i))
		content := fmt.Sprintf("content %d - %d", i, os.Getpid()) // Use PID for uniqueness
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("Commit %d", i))
	}

	// 2. Verify 'git sd status' is read-only: fresh commits show as Untracked.
	out := runGitSD(t, repoDir, "status")
	fmt.Println(out)
	if !strings.Contains(out, "—") {
		t.Fatalf("expected fresh commits to show as Untracked ('—') in status before tracking. Status output:\n%s", out)
	}
	logOutBeforeTrack := runGit(t, repoDir, "log", "-n", "3", "--format=%B")
	if strings.Contains(logOutBeforeTrack, "git-sd-id:") {
		t.Fatalf("status must not add trailers; found one before 'git sd track' ran")
	}

	// 3. Run 'git sd track' and verify trailers are added
	runGitSD(t, repoDir, "track")
	logOut := runGit(t, repoDir, "log", "-n", "3", "--format=%B")
	if !strings.Contains(logOut, "git-sd-id:") {
		t.Fatalf("trailers were not added to commit messages")
	}
	fmt.Println("Trailers verified in log.")

	// Regression test for B-002: running 'track' again must not duplicate trailers.
	runGitSD(t, repoDir, "track")
	logOut2 := runGit(t, repoDir, "log", "-n", "3", "--format=%B")
	if count := strings.Count(logOut2, "git-sd-id:"); count != 3 {
		t.Fatalf("trailer idempotency regression (B-002): expected exactly 3 'git-sd-id:' trailers after running track twice, found %d.\nLog:\n%s", count, logOut2)
	}
	fmt.Println("Trailer idempotency verified (track run twice, no duplicate trailers).")
}

func stepStackSetAssignment(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 3: Stack Set Assignment ---")
	out := runGitSD(t, repoDir, "assign", "feat-a:0-2")
	fmt.Println(out)

	// Verify status shows assigned
	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "feat-a") || !strings.Contains(out, "📦") {
		t.Fatalf("status does not show assigned stack set correctly")
	}
	fmt.Println("Assignment verified in status.")
}

func stepUnassignment(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario: Unassignment ---")
	out := runGitSD(t, repoDir, "assign", ":1")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	lines := strings.Split(out, "\n")
	var commit1Line, commit2Line string
	for _, line := range lines {
		if strings.Contains(line, "Commit 1") {
			commit1Line = line
		}
		if strings.Contains(line, "Commit 2") {
			commit2Line = line
		}
	}
	if commit1Line == "" || commit2Line == "" {
		t.Fatalf("could not find Commit 1/Commit 2 rows in status. Status output:\n%s", out)
	}
	if strings.Contains(commit2Line, "feat-a") || strings.Contains(commit2Line, "📦") {
		t.Fatalf("unassign failed: Commit 2 still shows feat-a/📦. Line: %s", commit2Line)
	}
	if !strings.Contains(commit1Line, "feat-a") || !strings.Contains(commit1Line, "📦") {
		t.Fatalf("unassign affected an unrelated commit: Commit 1 lost its feat-a assignment. Line: %s", commit1Line)
	}
	fmt.Println("Unassignment verified in status (Commit 2 unassigned, others untouched).")

	// Restore the assignment so downstream scenarios see all 3 commits in feat-a.
	runGitSD(t, repoDir, "assign", "feat-a:1")
}

func stepReorderingMove(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 4: Reordering (Move) ---")
	// Move commit 2 to index 0
	out := runGitSD(t, repoDir, "move", "2:0")
	fmt.Println(out)

	// Verify order in status
	out = runGitSD(t, repoDir, "status")
	// Commit 3 (which was at index 2) should now be at the top.
	// We'll check the message in the first row.
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "Commit 3") {
		t.Fatalf("move failed: Commit 3 is not at index 0. Status output:\n%s", out)
	}
	fmt.Println("Move verified in status.")
}

func stepSyncingAndPRCreation(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 5: Syncing & PR Creation ---")
	out := runGitSD(t, repoDir, "sync", "feat-a")
	fmt.Println(out)

	// Verify PR info in status
	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "https://github.com/") {
		t.Fatalf("status does not show PR URLs after sync. Status output:\n%s", out)
	}
	fmt.Println("Sync verified in status.")

	// Verify the git-sd-id trailer never reaches GitHub: git-sd no longer
	// writes its own Stack table into PR descriptions (GitHub's native Stacks
	// UI handles chain navigation instead), but it does strip each pushed
	// commit's trailer before push — confirm that actually happened.
	feataPRs := extractStackPRNumbers(out, "feat-a")
	if len(feataPRs) == 0 {
		t.Fatalf("could not find a feat-a PR number to verify trailer stripping")
	}
	prNumber := feataPRs[0]
	prRefsOut := runGit(t, repoDir, "pr", "view", prNumber, "--json", "baseRefName,headRefName")
	var prRefs struct {
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(prRefsOut), &prRefs); err != nil {
		t.Fatalf("failed to parse PR #%s refs: %v", prNumber, err)
	}
	runGit(t, repoDir, "fetch", "origin",
		fmt.Sprintf("%s:refs/remotes/origin/%s", prRefs.BaseRefName, prRefs.BaseRefName),
		fmt.Sprintf("%s:refs/remotes/origin/%s", prRefs.HeadRefName, prRefs.HeadRefName))
	// Scoped to base..head (NOT an unbounded `log FETCH_HEAD`, which would walk
	// this sacrificial repo's entire history — including years-old pre-migration
	// commits that legitimately have a git-sd-id trailer, a false positive).
	branchLog := runGit(t, repoDir, "log", "--format=%B",
		fmt.Sprintf("origin/%s..origin/%s", prRefs.BaseRefName, prRefs.HeadRefName))
	if strings.Contains(branchLog, "git-sd-id:") {
		t.Fatalf("PR #%s's pushed branch still contains a git-sd-id trailer; it must be stripped before push.\nLog:\n%s", prNumber, branchLog)
	}
	fmt.Println("Trailer stripping verified: pushed branch carries no git-sd-id trailer.")
}

func TestSDStackSetsOnOneBranchStayIsolated(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Multiple Stack Sets Isolation ---")

	// Create two independent sets from scratch in this clone: iso-a (the two
	// oldest commits) and iso-b (the two newest). Commits are indexed
	// oldest-first (index 0), so iso-a:0-1 and iso-b:2-3.
	for i := 1; i <= 4; i++ {
		filename := filepath.Join(repoDir, fmt.Sprintf("isofile%d.txt", i))
		content := fmt.Sprintf("content %d - %d", i, os.Getpid())
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("Iso Commit %d", i))
	}

	// Populate git-sd-ids / indices, then assign the two sets.
	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "assign", "iso-a:0-1")
	fmt.Println(out)
	out = runGitSD(t, repoDir, "assign", "iso-b:2-3")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "iso-a") || !strings.Contains(out, "iso-b") {
		t.Fatalf("status does not show both iso-a and iso-b stack sets coexisting. Status output:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	var commitBLine, commitALine string
	for _, line := range lines {
		if strings.Contains(line, "Iso Commit 3") {
			commitBLine = line
		}
		if strings.Contains(line, "Iso Commit 1") {
			commitALine = line
		}
	}
	if commitBLine == "" || commitALine == "" {
		t.Fatalf("could not find Iso Commit 1/Iso Commit 3 rows in status. Status output:\n%s", out)
	}
	if !strings.Contains(commitBLine, "iso-b") || strings.Contains(commitBLine, "iso-a") {
		t.Fatalf("Iso Commit 3 is not correctly isolated to iso-b. Line: %s", commitBLine)
	}
	if !strings.Contains(commitALine, "iso-a") || strings.Contains(commitALine, "iso-b") {
		t.Fatalf("Iso Commit 1 is not correctly isolated to iso-a. Line: %s", commitALine)
	}
	fmt.Println("Stack set isolation verified in status (iso-a and iso-b coexist without cross-contamination).")

	// Sync iso-a first and record its PRs, so we can prove syncing iso-b later
	// leaves them untouched.
	out = runGitSD(t, repoDir, "sync", "iso-a")
	fmt.Println(out)
	out = runGitSD(t, repoDir, "status")
	isoAPRsBefore := extractStackPRNumbers(out, "iso-a")
	if len(isoAPRsBefore) != 2 {
		t.Fatalf("expected 2 PRs for iso-a after sync, got %d. Status output:\n%s", len(isoAPRsBefore), out)
	}

	// Sync iso-b and confirm iso-a's existing PRs are untouched.
	out = runGitSD(t, repoDir, "sync", "iso-b")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	isoBPRs := extractStackPRNumbers(out, "iso-b")
	if len(isoBPRs) != 2 {
		t.Fatalf("expected 2 PRs for iso-b after sync, got %d. Status output:\n%s", len(isoBPRs), out)
	}
	isoAPRsAfter := extractStackPRNumbers(out, "iso-a")
	if strings.Join(isoAPRsAfter, ",") != strings.Join(isoAPRsBefore, ",") {
		t.Fatalf("syncing iso-b disturbed iso-a's PRs: before=%v after=%v", isoAPRsBefore, isoAPRsAfter)
	}
	fmt.Println("Sync isolation verified: syncing iso-b did not affect iso-a's PRs.")

	// rebase only ever retires commits it can confirm are actually finished
	// (landed/closed) — see TestSDRebasePreservesUnmergedSurvivorAfterPartialMerge.
	// Close both of iso-b's PRs, then confirm finishing iso-b (rebase + a
	// scoped sync) leaves the sibling set on the same branch untouched.
	for _, n := range isoBPRs {
		runGit(t, repoDir, "pr", "close", n)
	}

	out = runGitSD(t, repoDir, "rebase")
	fmt.Println(out)
	out = runGitSD(t, repoDir, "sync", "iso-b")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	if strings.Contains(out, "iso-b") {
		t.Fatalf("rebase failed to retire closed iso-b: status still shows iso-b. Status output:\n%s", out)
	}
	if isoAPRsFinal := extractStackPRNumbers(out, "iso-a"); strings.Join(isoAPRsFinal, ",") != strings.Join(isoAPRsBefore, ",") {
		t.Fatalf("finishing iso-b disturbed iso-a's PRs: before=%v after=%v. Status output:\n%s", isoAPRsBefore, isoAPRsFinal, out)
	}
	fmt.Println("Isolation verified: retiring iso-b left iso-a untouched.")
}

func TestSDSyncChainsEachStackSetIndependentlyNotOntoAnother(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Cross-Set Base Chaining (syncing two Stack Sets together)
	//
	// Unlike the isolation scenario above (which syncs each set by name,
	// separately), this exercises syncing two independent, interleaved Stack
	// Sets in a SINGLE call — the exact case where currentBase used to leak
	// across sets and chain the second set's PR onto the first set's branch.
	fmt.Println("--- Running Scenario: Cross-Set Base Chaining ---")
	for _, n := range []string{"X", "Y"} {
		filename := filepath.Join(repoDir, fmt.Sprintf("fileChain%s.txt", n))
		if err := os.WriteFile(filename, fmt.Appendf(nil, "chain %s - %d", n, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("Chain Test %s", n))
	}

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var idxChainX, idxChainY string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Chain Test X") {
			idxChainX = statusIndex(t, out, line)
		}
		if strings.Contains(line, "Chain Test Y") {
			idxChainY = statusIndex(t, out, line)
		}
	}
	if idxChainX == "" || idxChainY == "" {
		t.Fatalf("could not find indices for Chain Test X/Y. Status output:\n%s", out)
	}

	out = runGitSD(t, repoDir, "assign", fmt.Sprintf("chainx:%s", idxChainX), fmt.Sprintf("chainy:%s", idxChainY))
	fmt.Println(out)

	// Sync BOTH together in one call — this is what used to leak chainx's
	// branch into chainy's PR base.
	out = runGitSD(t, repoDir, "sync", "--all")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	chainYPRs := extractStackPRNumbers(out, "chainy")
	if len(chainYPRs) != 1 {
		t.Fatalf("expected exactly 1 PR for chainy, got %d. Status output:\n%s", len(chainYPRs), out)
	}

	baseOut := runGit(t, repoDir, "pr", "view", chainYPRs[0], "--json", "baseRefName")
	var chainYBase struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(baseOut), &chainYBase); err != nil {
		t.Fatalf("failed to parse chainy PR #%s base: %v", chainYPRs[0], err)
	}
	if chainYBase.BaseRefName != "main" {
		t.Fatalf("cross-set chaining bug: expected chainy's PR to base on 'main', got %q (leaked chainx's branch)", chainYBase.BaseRefName)
	}
	fmt.Println("Cross-set base chaining verified: chainy's PR based on main, not chainx's branch.")
}

// TestSDInterleavedStackSetsMergeIndependently covers TODO.md #4. Unlike
// TestSDStackSetsOnOneBranchStayIsolated/
// TestSDSyncChainsEachStackSetIndependentlyNotOntoAnother (each set's
// commits sit in one contiguous block), the two sets here are genuinely
// interleaved (A, B, A, B) — and unlike either of those, this one actually
// merges each set to completion, not just sync. Confirms merging one
// set doesn't touch, include, or disturb the other's commits/PRs/content.
func TestSDInterleavedStackSetsMergeIndependently(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Interleaved Stack Sets Merge Independently ---")

	// Physical order: interA1, interB1, interA2, interB2.
	for _, c := range []struct{ file, msg string }{
		{"intera1.txt", "InterA Commit 1"},
		{"interb1.txt", "InterB Commit 1"},
		{"intera2.txt", "InterA Commit 2"},
		{"interb2.txt", "InterB Commit 2"},
	} {
		if err := os.WriteFile(filepath.Join(repoDir, c.file), fmt.Appendf(nil, "%s %d", c.msg, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", c.file)
		runGit(t, repoDir, "commit", "-m", c.msg)
	}

	// Non-contiguous assignment: intera at indices 0,2; interb at 1,3.
	runGitSD(t, repoDir, "assign", "intera:0,2", "interb:1,3")
	runGitSD(t, repoDir, "sync", "--all")

	out := runGitSD(t, repoDir, "status")
	prsA := extractStackPRNumbers(out, "intera")
	prsB := extractStackPRNumbers(out, "interb")
	if len(prsA) != 2 {
		t.Fatalf("expected 2 PRs for intera, got %d. Status:\n%s", len(prsA), out)
	}
	if len(prsB) != 2 {
		t.Fatalf("expected 2 PRs for interb, got %d. Status:\n%s", len(prsB), out)
	}

	// Each PR's diff must be isolated to its OWN set's files — the real proof
	// interleaving didn't leak content across sets during sync.
	for _, n := range prsA {
		if diff := runGit(t, repoDir, "pr", "diff", n, "--name-only"); strings.Contains(diff, "interb") {
			t.Fatalf("intera PR #%s's diff leaked interb content:\n%s", n, diff)
		}
	}
	for _, n := range prsB {
		if diff := runGit(t, repoDir, "pr", "diff", n, "--name-only"); strings.Contains(diff, "intera") {
			t.Fatalf("interb PR #%s's diff leaked intera content:\n%s", n, diff)
		}
	}
	fmt.Println("Interleaved sync verified: each set's PR diffs are isolated to their own files.")

	// Merge intera ALONE; interb's PRs must remain completely untouched.
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "intera")
	if mergeErr != nil {
		t.Fatalf("git sd merge intera failed: %v\nOutput: %s", mergeErr, mergeOut)
	}
	for _, n := range prsA {
		if state := prState(t, repoDir, n); state != "MERGED" {
			t.Fatalf("expected intera PR #%s to be MERGED, got %q", n, state)
		}
	}
	for _, n := range prsB {
		if state := prState(t, repoDir, n); state != "OPEN" {
			t.Fatalf("merging intera disturbed interb PR #%s: expected OPEN, got %q", n, state)
		}
	}
	fmt.Println("Merging intera left interb's PRs completely untouched (still OPEN).")

	// interb must still sync/merge cleanly afterward. Re-sync first — not
	// strictly required (these sets share no chain), but confirms rather than
	// assumes that an unrelated merge left nothing for interb's own sync to
	// stumble over.
	runGitSD(t, repoDir, "sync", "interb")
	mergeOut, mergeErr = runGitSDErr(repoDir, "merge", "interb")
	if mergeErr != nil {
		t.Fatalf("git sd merge interb failed: %v\nOutput: %s", mergeErr, mergeOut)
	}
	for _, n := range prsB {
		if state := prState(t, repoDir, n); state != "MERGED" {
			t.Fatalf("expected interb PR #%s to be MERGED, got %q", n, state)
		}
	}
	fmt.Println("Interleaved stack sets merged independently: both landed cleanly with no cross-contamination.")

	// merge is GitHub-only: drop the merged commits locally, then flush the
	// merged PRs' queued remote branch deletions.
	runGitSD(t, repoDir, "rebase")
	runGitSDErr(repoDir, "sync")
}

// stepBranchNamingSlug verifies branch names follow the hardcoded
// subject-slug + git-sd-id suffix scheme (there is no more configurable
// sd.pr.branch-name-format).
func stepBranchNamingSlug(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario: Branch Naming Slug ---")
	out := runGitSD(t, repoDir, "status")
	feataPRs := extractStackPRNumbers(out, "feat-a")
	if len(feataPRs) == 0 {
		t.Fatalf("no feat-a PRs found to verify branch naming. Status output:\n%s", out)
	}
	headRef := prHeadRef(t, repoDir, feataPRs[0])
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*-s[a-z0-9]{4}$`).MatchString(headRef) {
		t.Fatalf("branch name %q does not match the subject-slug + git-sd-id scheme", headRef)
	}
	fmt.Printf("Branch naming slug verified: %q.\n", headRef)
}

func stepMerging(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 8: Merging ---")
	// To merge, we need to find every PR number in the feat-a stack from status.
	out, _ := runGitSDErr(repoDir, "status")
	stackPRNumbers := extractStackPRNumbers(out, "feat-a")

	if len(stackPRNumbers) == 0 {
		t.Fatalf("could not find any PR numbers for feat-a in status output")
	}

	headBeforeMerge := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	fmt.Printf("Attempting to approve and merge PRs %v...\n", stackPRNumbers)
	// We might need to wait a bit for CI (though there is none)
	// and we might need to approve each PR in the stack.
	for _, n := range stackPRNumbers {
		if out, err := runGitErr(repoDir, "pr", "review", n, "--approve"); err != nil {
			fmt.Printf("Warning: Failed to approve PR #%s (might be self-approval restriction): %v\nOutput: %s\n", n, err, out)
		}
	}

	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "feat-a")
	if mergeErr != nil {
		t.Fatalf("git sd merge failed: %v\nOutput: %s", mergeErr, mergeOut)
	}
	fmt.Println(mergeOut)

	// GitHub's native async stack merge merges every PR in the range
	// individually (server-side rebasing each survivor as it goes) rather
	// than git-sd squashing them into one consolidated PR — every PR in the
	// stack should end up MERGED, none CLOSED.
	mergedCount := 0
	for _, n := range stackPRNumbers {
		viewOut := runGit(t, repoDir, "pr", "view", n, "--json", "state")
		var info struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal([]byte(viewOut), &info); err != nil {
			t.Fatalf("failed to parse PR #%s info: %v", n, err)
		}
		if info.State != "MERGED" {
			t.Fatalf("expected PR #%s to be MERGED (native stack merge merges every PR in range individually), got %q", n, info.State)
		}
		mergedCount++
	}
	fmt.Printf("Merge verified: all %d PR(s) in the stack merged individually.\n", mergedCount)

	// merge is GitHub-only: the local branch must be untouched until rebase.
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); got != headBeforeMerge {
		t.Fatalf("git sd merge must not touch the local branch: HEAD was %s, now %s", headBeforeMerge, got)
	}
	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "feat-a") {
		t.Fatalf("expected status to still list the merged feat-a commits before rebase. Status output:\n%s", out)
	}
	fmt.Println("Local branch untouched by merge; merged commits still listed until rebase.")
}

// makeCommits creates n commits (msgPrefix 1..n) with unique content in dir.
func makeCommits(t *testing.T, repoDir, filePrefix, msgPrefix string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		filename := filepath.Join(repoDir, fmt.Sprintf("%s%d.txt", filePrefix, i))
		content := fmt.Sprintf("%s %d - %d", msgPrefix, i, os.Getpid())
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("%s %d", msgPrefix, i))
	}
}

// TestSDStatusShowsHierarchicalSubIndexLabel verifies that attaching a
// commit as a sub-commit (`move <src>:<target>.<rank>`) shows the correct
// "N.R" hierarchical label in status, and that it isn't just a label —
// syncing folds it into the parent's own PR (not a separate one).
func TestSDStatusShowsHierarchicalSubIndexLabel(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "hier", "Hier Commit", 2)
	runGitSD(t, repoDir, "assign", "hier:0")

	out := runGitSD(t, repoDir, "move", "1:0.1") // attach commit 1 as sub-commit "0.1" of commit 0
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "0.1") {
		t.Fatalf("hierarchical indexing failed: 0.1 not found in status. Status output:\n%s", out)
	}

	runGitSD(t, repoDir, "sync")
	out = runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "hier")
	if len(prs) != 1 {
		t.Fatalf("expected the sub-commit to fold into a single PR (not its own), got %d. Status:\n%s", len(prs), out)
	}
	fmt.Println("Hierarchical indexing verified in status and confirmed to fold into the parent's PR.")
}

// TestSDMoveToSubIndexShowsSubIndexLabel verifies that `move 2:0.2` reorders a
// commit to a sub-index and shows the "0.2" label in status. Local-only, self-contained.
func TestSDMoveToSubIndexShowsSubIndexLabel(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "movesub", "MoveSub Commit", 3)
	runGitSD(t, repoDir, "track") // assign git-sd-ids
	runGitSD(t, repoDir, "assign", "movesub:0-2")

	// "MoveSub Commit 1" is at index 0 -- dest names the target's OWN index
	// directly (not an insertion slot), so attaching under it is `move 2:0.2`.
	out := runGitSD(t, repoDir, "move", "2:0.2")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "0.2") {
		t.Fatalf("move to sub-index failed: 0.2 not found in status. Status output:\n%s", out)
	}
	fmt.Println("Move to sub-index verified in status.")
}

// TestSDMoveAllowsDormantAttachmentToUnassignedTarget verifies that `move`'s
// ranked-attach form succeeds even when the target isn't assigned to any
// Stack Set — the attachment sits dormant (StackSet mirrors the target's,
// which is "") until the target itself gets assigned (see SPEC.md's move section).
// This supersedes the old B-015-era expectation that such a move must be
// rejected outright: that rejection existed only because the pre-fix
// targeting scheme could silently produce a WRONG parent, not because a
// dormant attachment is itself invalid.
func TestSDMoveAllowsDormantAttachmentToUnassignedTarget(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "dormant", "Dormant Commit", 3)
	runGitSD(t, repoDir, "track") // assign git-sd-ids, no Stack Set assigned to any of them

	out, err := runGitSDErr(repoDir, "move", "2:0.1")
	if err != nil {
		t.Fatalf("expected a dormant attach to an unassigned target to succeed, got error: %v\nOutput:\n%s", err, out)
	}

	out = runGitSD(t, repoDir, "status")
	var subLine string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Dormant Commit 3") {
			subLine = line
		}
	}
	if !strings.Contains(subLine, "0.1") {
		t.Fatalf("expected the commit to show as sub-commit 0.1, got line: %s", subLine)
	}
	if strings.Contains(subLine, "📦") {
		t.Fatalf("expected the dormant attachment to show no Stack Set (unassigned target), got line: %s", subLine)
	}

	// Now assign the parent -- the dormant sub-commit must pick up the
	// assignment automatically (the cascading rule).
	runGitSD(t, repoDir, "assign", "dormant:0")
	out = runGitSD(t, repoDir, "status")
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Dormant Commit 3") {
			subLine = line
		}
	}
	if !strings.Contains(subLine, "dormant") || !strings.Contains(subLine, "0.1") {
		t.Fatalf("expected the dormant sub-commit to pick up the parent's assignment, got line: %s", subLine)
	}
	fmt.Println("Dormant attachment verified: succeeded while unassigned, activated once the parent was assigned.")
}

// TestSDMoveToSubIndexAutoTracksUntrackedCommit verifies that moving an
// UNTRACKED commit (no git-sd-id yet) to a sub-index auto-tracks it (B-016)
// rather than silently dropping the attachment — here the parent exists and
// is valid, but the *moved* commit itself was never tracked, so persisting
// its ParentID/SubRank/StackSet requires `move` to assign it a git-sd-id
// first, the same way `assign` already does for the commits it references.
func TestSDMoveToSubIndexAutoTracksUntrackedCommit(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)

	// A single tracked, assigned top-level commit — a valid attachment target.
	makeCommits(t, repoDir, "sdxuparent", "SdxuParent Commit", 1)
	runGitSD(t, repoDir, "assign", "sdxu:0")

	// A second commit created AFTER assigning the parent, so it is never
	// tracked (no 'track'/'assign'/'status' call has touched it).
	untrackedFile := filepath.Join(repoDir, "sdxu-untracked.txt")
	if err := os.WriteFile(untrackedFile, fmt.Appendf(nil, "untracked %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", untrackedFile)
	runGit(t, repoDir, "commit", "-m", "Sdxu Untracked Commit")

	// Attach the untracked commit (index 1) as a sub-commit of the tracked
	// parent at index 0 -- dest names the parent directly.
	out := runGitSD(t, repoDir, "move", "1:0.1")
	fmt.Println(out)

	// The commit must come back tracked, grouped under the parent's Stack Set
	// at sub-index 1.1 — not silently dropped as an ungrouped Untracked row.
	out = runGitSD(t, repoDir, "status")
	var sdxuLine string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Sdxu Untracked Commit") {
			sdxuLine = line
		}
	}
	if sdxuLine == "" {
		t.Fatalf("could not find the moved commit's row in status:\n%s", out)
	}
	if !strings.Contains(sdxuLine, "0.1") || !strings.Contains(sdxuLine, "sdxu") {
		t.Fatalf("expected the moved commit to show sub-index 0.1 under 'sdxu', got line: %s", sdxuLine)
	}
	if strings.Contains(sdxuLine, "—") {
		t.Fatalf("expected the moved commit to be auto-tracked (no '—' Untracked marker), got line: %s", sdxuLine)
	}
	fmt.Println("Move-to-sub-index of an untracked commit correctly auto-tracked it and recorded the attachment.")
}

// stepRebaseAndSync runs after stepMerging merged all of feat-a: merge is
// GitHub-only, so rebase is what verifies the PRs landed and drops/retires
// the merged commits locally, and the next sync deletes the merged PRs'
// queued remote branches.
func stepRebaseAndSync(t *testing.T, repoDir string) {
	fmt.Println("--- Running Scenario 9: Rebase & Sync Teardown ---")
	out := runGitSD(t, repoDir, "rebase")
	fmt.Println(out)
	if strings.Contains(out, "Already up to date.") || strings.Contains(out, "dropped 0 landed PR(s)") {
		t.Fatalf("expected rebase after 'git sd merge' to drop the merged commits, got:\n%s", out)
	}
	if st := runGitSD(t, repoDir, "status"); strings.Contains(st, "feat-a") {
		t.Fatalf("expected rebase to drop the merged feat-a commits from status. Status output:\n%s", st)
	}

	// The branches this sync must delete are the ones rebase queued in this
	// clone's metadata (other tests share the remote, so its branch list as a
	// whole says nothing about this sync).
	queued := pendingBranchDeletions(t, repoDir, "feat-a")
	if len(queued) == 0 {
		t.Fatalf("expected rebase to queue feat-a's merged branches for deletion")
	}
	out = runGitSD(t, repoDir, "sync", "feat-a")
	fmt.Println(out)
	if reported := deletedBranchCount(t, out); reported != len(queued) {
		t.Fatalf("expected sync to delete the %d queued branches, it reported %d. Output:\n%s", len(queued), reported, out)
	}
	branches, err := gitSDRemoteBranches(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range queued {
		if slices.Contains(branches, b) {
			t.Fatalf("sync reported deleting %s, but it is still on the remote", b)
		}
	}

	out = runGitSD(t, repoDir, "status")
	if strings.Contains(out, "feat-a") || strings.Contains(out, "📦") {
		t.Fatalf("status still shows the merged stack set. Status output:\n%s", out)
	}
	fmt.Println("Rebase/sync teardown verified.")
}

// TestSDMergeIsGitHubOnlyAndWorksWithUncommittedChanges verifies `git sd
// merge` never touches the local branch or working tree: it succeeds with a
// dirty tree, leaves HEAD and the uncommitted edit alone, and only a later
// `git sd rebase` drops the merged commit locally.
func TestSDMergeIsGitHubOnlyAndWorksWithUncommittedChanges(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Merge Is GitHub-Only And Works With Uncommitted Changes ---")

	makeCommits(t, repoDir, "ghonly", "GhOnly Commit", 1)
	runGitSD(t, repoDir, "assign", "ghonly:0")
	runGitSD(t, repoDir, "sync", "ghonly")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "ghonly")
	if len(prs) != 1 {
		t.Fatalf("expected exactly 1 PR for ghonly, got %d. Status output:\n%s", len(prs), out)
	}
	prNumber := prs[0]
	runGitErr(repoDir, "pr", "review", prNumber, "--approve") // best-effort; own PR can't be self-approved

	// Dirty a tracked file without committing.
	dirtyFile := filepath.Join(repoDir, "ghonly1.txt")
	const dirtyContent = "uncommitted edit"
	if err := os.WriteFile(dirtyFile, []byte(dirtyContent), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	headBefore := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "ghonly")
	if mergeErr != nil {
		t.Fatalf("git sd merge failed with uncommitted changes: %v\nOutput: %s", mergeErr, mergeOut)
	}
	fmt.Println(mergeOut)

	if state := prState(t, repoDir, prNumber); state != "MERGED" {
		t.Fatalf("expected PR #%s to be MERGED, got %q", prNumber, state)
	}
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("git sd merge must not touch the local branch: HEAD was %s, now %s", headBefore, got)
	}
	data, err := os.ReadFile(dirtyFile)
	if err != nil || string(data) != dirtyContent {
		t.Fatalf("git sd merge clobbered the uncommitted change: content %q, err %v", data, err)
	}
	if !strings.Contains(runGitSD(t, repoDir, "status"), "GhOnly Commit 1") {
		t.Fatalf("expected the merged commit to still be listed before rebase")
	}

	// rebase rewrites the branch, so discard the uncommitted edit first.
	runGit(t, repoDir, "checkout", "--", "ghonly1.txt")
	runGitSD(t, repoDir, "rebase")
	if st := runGitSD(t, repoDir, "status"); strings.Contains(st, "GhOnly Commit 1") {
		t.Fatalf("expected rebase to drop the merged commit. Status output:\n%s", st)
	}
	fmt.Println("Merge verified GitHub-only: dirty tree and HEAD untouched until rebase.")

	runGitSDErr(repoDir, "sync", "ghonly")
}

// TestSDMergeDelegatesApprovalEnforcementToGitHub verifies that git-sd itself
// no longer checks approval/CI before submitting a merge — that's now
// entirely GitHub's call, via whatever branch protection is configured on
// the sacrificial repo's base branch (see DEVELOPMENT.md). This test
// tolerates either outcome (blocked or allowed) and just asserts git-sd's
// reported result matches the PR's actual final state, rather than asserting
// a specific refusal the way the pre-migration safeguard check did.
func TestSDMergeDelegatesApprovalEnforcementToGitHub(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Merge Safeguards Delegated To Branch Protection ---")
	safeguardFile := filepath.Join(repoDir, "file8.txt")
	if err := os.WriteFile(safeguardFile, fmt.Appendf(nil, "content 8 - %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", safeguardFile)
	runGit(t, repoDir, "commit", "-m", "Commit 8")

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var idx8 string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Commit 8") {
			idx8 = statusIndex(t, out, line)
		}
	}
	if idx8 == "" {
		t.Fatalf("could not find index for Commit 8. Status output:\n%s", out)
	}

	out = runGitSD(t, repoDir, "assign", fmt.Sprintf("safechk:%s", idx8))
	fmt.Println(out)

	out = runGitSD(t, repoDir, "sync", "safechk")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	safechkPRs := extractStackPRNumbers(out, "safechk")
	if len(safechkPRs) != 1 {
		t.Fatalf("expected exactly 1 PR for safechk, got %d. Status output:\n%s", len(safechkPRs), out)
	}
	safechkPR := safechkPRs[0]

	// Deliberately do NOT approve this PR. git-sd itself no longer checks
	// approval — it just submits to GitHub's async stack-merge API and
	// reports whatever GitHub decides.
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "safechk")

	prStateOut := runGit(t, repoDir, "pr", "view", safechkPR, "--json", "state")
	var prState struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(prStateOut), &prState); err != nil {
		t.Fatalf("failed to parse safechk PR state: %v", err)
	}

	if mergeErr != nil {
		if prState.State != "OPEN" {
			t.Fatalf("merge reported an error but PR #%s ended up %q, not OPEN; inconsistent state. Output:\n%s", safechkPR, prState.State, mergeOut)
		}
		fmt.Printf("Branch protection blocked the unapproved merge (repo has approval rules configured): %v\n", mergeErr)
	} else {
		if prState.State != "MERGED" {
			t.Fatalf("merge reported success but PR #%s ended up %q, not MERGED; inconsistent state. Output:\n%s", safechkPR, prState.State, mergeOut)
		}
		fmt.Println("No branch protection configured on the sacrificial repo's base branch — merge proceeded without approval, as git-sd now delegates that decision entirely to GitHub. See DEVELOPMENT.md.")
	}

	// merge is GitHub-only: drop any merged commit locally before the final sync.
	runGitSD(t, repoDir, "rebase")
	runGitSDErr(repoDir, "sync", "safechk")
}

// TestSDMergeSucceedsWithSecondReviewerApproval exercises the real
// multi-account approve-then-merge round trip. GitHub forbids
// self-approval, so every other merge scenario in this suite either
// tolerates a failed self-approve attempt or relies on there being no
// branch protection to enforce anyway. This is the one scenario where the
// PR is genuinely approved by a different GitHub account
// (GIT_SD_TEST_REVIEWER_TOKEN) before merging — skips cleanly if that
// token isn't set (see DEVELOPMENT.md for how to configure one).
func TestSDMergeSucceedsWithSecondReviewerApproval(t *testing.T) {
	if reviewerToken == "" {
		t.Skip("Skipping: GIT_SD_TEST_REVIEWER_TOKEN not set (see DEVELOPMENT.md for how to configure a second reviewer account).")
	}
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Merge Succeeds With Second Reviewer Approval ---")

	makeCommits(t, repoDir, "tworev", "TwoRev Commit", 1)
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "assign", "tworev:0")
	runGitSD(t, repoDir, "sync", "tworev")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "tworev")
	if len(prs) != 1 {
		t.Fatalf("expected exactly 1 PR for tworev, got %d. Status output:\n%s", len(prs), out)
	}
	prNumber := prs[0]

	if out, err := approveAsReviewer(repoDir, prNumber); err != nil {
		t.Fatalf("reviewer approval failed: %v\nOutput: %s", err, out)
	}
	fmt.Printf("PR #%s approved by the reviewer account.\n", prNumber)

	// Confirm the approval is real (a different account, not self-approval)
	// before merging, so a failure below can't be mistaken for "the approval
	// never actually landed."
	if got := prReviewDecision(t, repoDir, prNumber); got != "APPROVED" {
		t.Fatalf("expected PR #%s to show reviewDecision=APPROVED after a real second-account review, got %q", prNumber, got)
	}

	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "tworev")
	if mergeErr != nil {
		t.Fatalf("git sd merge failed even though the PR was genuinely approved by a different account: %v\nOutput: %s", mergeErr, mergeOut)
	}

	mergedView := runGit(t, repoDir, "pr", "view", prNumber, "--json", "state")
	var mergedInfo struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(mergedView), &mergedInfo); err != nil {
		t.Fatalf("failed to parse merged PR #%s info: %v", prNumber, err)
	}
	if mergedInfo.State != "MERGED" {
		t.Fatalf("expected PR #%s to be MERGED, got %q", prNumber, mergedInfo.State)
	}
	fmt.Printf("Merge succeeded: PR #%s approved by a real second account and merged.\n", prNumber)

	// merge is GitHub-only: drop any merged commit locally before the final sync.
	runGitSD(t, repoDir, "rebase")
	runGitSDErr(repoDir, "sync", "tworev")
}

// TestSDNewSetViaBareSyncPreservesApprovedExistingSet answers TODO.md #5/#8:
// does adding a second, unrelated Stack Set and running a bare `git sd sync`
// (no set name) disturb an already-approved existing set? This isn't testing
// GitHub's own guarantees (a review's APPROVED state is never auto-dismissed
// by a push in any GitHub config) — it's testing git-sd's own sync.go, whose
// Pass 0/1/2 have no "did anything change" check: a bare sync unconditionally
// reprocesses EVERY set, including ones with nothing to do. This confirms
// that reprocessing reuses the same PR (git-sd-id-based branch identity)
// rather than anything more disruptive.
func TestSDNewSetViaBareSyncPreservesApprovedExistingSet(t *testing.T) {
	if reviewerToken == "" {
		t.Skip("Skipping: GIT_SD_TEST_REVIEWER_TOKEN not set (see DEVELOPMENT.md for how to configure a second reviewer account).")
	}
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: New Set Via Bare Sync Preserves An Approved Existing Set ---")

	makeCommits(t, repoDir, "twoseta", "TwoSetA Commit", 1)
	runGitSD(t, repoDir, "assign", "twoseta:0")
	runGitSD(t, repoDir, "sync", "twoseta")

	out := runGitSD(t, repoDir, "status")
	prsA := extractStackPRNumbers(out, "twoseta")
	if len(prsA) != 1 {
		t.Fatalf("expected exactly 1 PR for twoseta, got %d. Status output:\n%s", len(prsA), out)
	}
	prA := prsA[0]

	if out, err := approveAsReviewer(repoDir, prA); err != nil {
		t.Fatalf("reviewer approval failed: %v\nOutput: %s", err, out)
	}
	before := prInfo(t, repoDir, prA)
	if before.ReviewDecision != "APPROVED" {
		t.Fatalf("expected PR #%s to be APPROVED before the bare sync, got %q", prA, before.ReviewDecision)
	}
	headBefore := before.HeadRefName
	baseBefore := before.BaseRefName

	// Add a second, unrelated set and sync with NO set name — this is what
	// reprocesses twoseta too, even though nothing about it changed.
	makeCommits(t, repoDir, "twosetb", "TwoSetB Commit", 1)
	runGitSD(t, repoDir, "assign", "twosetb:1")
	runGitSD(t, repoDir, "sync")

	out = runGitSD(t, repoDir, "status")
	prsAAfter := extractStackPRNumbers(out, "twoseta")
	if len(prsAAfter) != 1 || prsAAfter[0] != prA {
		t.Fatalf("twoseta's PR was not reused across the bare sync: before=%s after=%v", prA, prsAAfter)
	}
	after := prInfo(t, repoDir, prA)
	if after.HeadRefName != headBefore {
		t.Fatalf("twoseta's PR head ref changed after an unrelated bare sync: before=%q after=%q", headBefore, after.HeadRefName)
	}
	if after.BaseRefName != baseBefore {
		t.Fatalf("twoseta's PR base changed after an unrelated bare sync: before=%q after=%q", baseBefore, after.BaseRefName)
	}
	if after.ReviewDecision != "APPROVED" {
		t.Fatalf("twoseta's approval was lost after an unrelated bare sync touched it: got %q", after.ReviewDecision)
	}
	fmt.Printf("PR #%s survived the bare sync unchanged and still shows APPROVED.\n", prA)

	// Prove the approval is still functionally valid, not just cosmetically
	// unchanged: the merge should actually succeed.
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "twoseta")
	if mergeErr != nil {
		t.Fatalf("git sd merge failed even though twoseta's PR was approved and untouched: %v\nOutput: %s", mergeErr, mergeOut)
	}
	if state := prState(t, repoDir, prA); state != "MERGED" {
		t.Fatalf("expected PR #%s to be MERGED, got %q", prA, state)
	}
	fmt.Printf("Merge succeeded: PR #%s merged after surviving an unrelated bare sync.\n", prA)

	// merge is GitHub-only: drop any merged commit locally before the final sync.
	runGitSD(t, repoDir, "rebase")
	runGitSDErr(repoDir, "sync", "twoseta")
}

// TestSDMoveReorderWithinSetPreservesPRIdentityAndApproval covers the other
// half of TODO.md #5/#8: reordering commits within a Stack Set via `move`
// genuinely changes a PR's base (the whole point of a restructure), but must
// preserve the PR's own identity (git-sd-id-based branch naming means the
// same PR is reused, not recreated) and therefore its approval. This also
// regression-guards the Pass 0 fix in sync.go (unconditionally `Unstack`
// before any base edit) — a reorder is exactly the restructure case that fix
// exists for; without it, this would fail with GitHub's "cannot change the
// base branch because the pull request is part of a stack".
func TestSDMoveReorderWithinSetPreservesPRIdentityAndApproval(t *testing.T) {
	if reviewerToken == "" {
		t.Skip("Skipping: GIT_SD_TEST_REVIEWER_TOKEN not set (see DEVELOPMENT.md for how to configure a second reviewer account).")
	}
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Move Reorder Within A Set Preserves PR Identity And Approval ---")

	makeCommits(t, repoDir, "reorderset", "Reorder Commit", 2)
	runGitSD(t, repoDir, "assign", "reorderset:0-1")
	runGitSD(t, repoDir, "sync", "reorderset")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "reorderset")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs for reorderset, got %d. Status output:\n%s", len(prs), out)
	}
	bottomPR := prs[0] // index 0: bases on main today

	if base := prBaseRef(t, repoDir, bottomPR); base != "main" {
		t.Fatalf("expected the bottom PR #%s to base on main before the reorder, got %q", bottomPR, base)
	}

	// Approve the bottom PR specifically — it's about to become the TOP of
	// the stack, so its base will visibly change.
	if out, err := approveAsReviewer(repoDir, bottomPR); err != nil {
		t.Fatalf("reviewer approval failed: %v\nOutput: %s", err, out)
	}
	before := prInfo(t, repoDir, bottomPR)
	if before.ReviewDecision != "APPROVED" {
		t.Fatalf("expected PR #%s to be APPROVED before the reorder, got %q", bottomPR, before.ReviewDecision)
	}
	headBefore := before.HeadRefName

	// Swap the two top-level commits' physical order.
	runGitSD(t, repoDir, "move", "0:1")
	runGitSD(t, repoDir, "sync", "reorderset")

	out = runGitSD(t, repoDir, "status")
	prsAfter := extractStackPRNumbers(out, "reorderset")
	if len(prsAfter) != 2 {
		t.Fatalf("expected still 2 PRs for reorderset after the reorder, got %d. Status output:\n%s", len(prsAfter), out)
	}

	// The originally-approved PR must be the SAME PR (git-sd-id-based branch
	// identity), not a new one.
	after := prInfo(t, repoDir, bottomPR)
	if after.HeadRefName != headBefore {
		t.Fatalf("PR #%s's head ref changed after the reorder (it should be the same PR): before=%q after=%q", bottomPR, headBefore, after.HeadRefName)
	}
	// Its base has genuinely changed: it's now the top of the stack.
	if after.BaseRefName == "main" {
		t.Fatalf("expected PR #%s's base to change after becoming the top of the stack, but it still bases on main", bottomPR)
	}
	if after.ReviewDecision != "APPROVED" {
		t.Fatalf("PR #%s's approval was lost after a reorder that only changed its base, not its own content: got %q", bottomPR, after.ReviewDecision)
	}
	fmt.Printf("PR #%s survived the reorder: same PR, base moved to %q, still APPROVED.\n", bottomPR, after.BaseRefName)

	// Completeness check: the whole set still merges cleanly after the reorder.
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "reorderset")
	if mergeErr != nil {
		t.Fatalf("git sd merge failed after the reorder: %v\nOutput: %s", mergeErr, mergeOut)
	}
	for _, n := range prsAfter {
		if state := prState(t, repoDir, n); state != "MERGED" {
			t.Fatalf("expected PR #%s to be MERGED after merging the whole reordered set, got %q", n, state)
		}
	}
	fmt.Println("Reorder-then-merge verified: both PRs merged after the reorder.")

	// merge is GitHub-only: drop any merged commit locally before the final sync.
	runGitSD(t, repoDir, "rebase")
	runGitSDErr(repoDir, "sync", "reorderset")
}

func TestSDAmendThenResyncUpdatesDownstreamPR(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Amend-and-Resync Propagation ---")
	for _, n := range []string{"A", "B"} {
		filename := filepath.Join(repoDir, fmt.Sprintf("fileAmend%s.txt", n))
		if err := os.WriteFile(filename, fmt.Appendf(nil, "original %s - %d", n, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("Amend Test %s", n))
	}

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var idxA, idxB string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Amend Test A") {
			idxA = statusIndex(t, out, line)
		}
		if strings.Contains(line, "Amend Test B") {
			idxB = statusIndex(t, out, line)
		}
	}
	if idxA == "" || idxB == "" {
		t.Fatalf("could not find indices for Amend Test A/B. Status output:\n%s", out)
	}

	out = runGitSD(t, repoDir, "assign", fmt.Sprintf("amendck:%s-%s", idxA, idxB))
	fmt.Println(out)

	out = runGitSD(t, repoDir, "sync", "amendck")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	amendckPRsBefore := extractStackPRNumbers(out, "amendck")
	if len(amendckPRsBefore) != 2 {
		t.Fatalf("expected 2 PRs for amendck before amend, got %d. Status output:\n%s", len(amendckPRsBefore), out)
	}

	hashAOut := runGit(t, repoDir, "rev-parse", "HEAD~1")
	hashA := strings.TrimSpace(hashAOut)
	branchOut := runGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	currentBranch := strings.TrimSpace(branchOut)

	// Amend commit A in place (checked out detached at A), then rebase B on
	// top of the amendment with a REAL rebase, so B's diff is correctly
	// recomputed via cherry-pick rather than git-sd's own tree-reuse rebuild.
	runGit(t, repoDir, "checkout", hashA)
	if err := os.WriteFile(filepath.Join(repoDir, "fileAmendA.txt"), []byte("AMENDED content A"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "fileAmendA.txt")
	runGit(t, repoDir, "commit", "--amend", "--no-edit")
	amendedAHashOut := runGit(t, repoDir, "rev-parse", "HEAD")
	amendedAHash := strings.TrimSpace(amendedAHashOut)

	runGit(t, repoDir, "rebase", "--onto", amendedAHash, hashA, currentBranch)

	// Re-sync and confirm the same PRs are reused (force-pushed), not recreated.
	out = runGitSD(t, repoDir, "sync", "amendck")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	amendckPRsAfter := extractStackPRNumbers(out, "amendck")
	if len(amendckPRsAfter) != 2 || strings.Join(amendckPRsAfter, ",") != strings.Join(amendckPRsBefore, ",") {
		t.Fatalf("expected the same 2 PR numbers to be reused after re-sync, before=%v after=%v", amendckPRsBefore, amendckPRsAfter)
	}

	// The downstream (B) branch should now carry the amended content of A,
	// proving the rebase + re-sync propagated the change instead of leaving
	// a stale push.
	prInfoOut := runGit(t, repoDir, "pr", "view", amendckPRsAfter[1], "--json", "headRefName")
	var prInfo struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(prInfoOut), &prInfo); err != nil {
		t.Fatalf("failed to parse head ref for PR #%s: %v", amendckPRsAfter[1], err)
	}
	runGit(t, repoDir, "fetch", "origin", prInfo.HeadRefName)
	downstreamContent := runGit(t, repoDir, "show", "FETCH_HEAD:fileAmendA.txt")
	if !strings.Contains(downstreamContent, "AMENDED content A") {
		t.Fatalf("amend did not propagate downstream: expected 'AMENDED content A' in %s's fileAmendA.txt, got:\n%s", prInfo.HeadRefName, downstreamContent)
	}
	fmt.Println("Amend-and-resync propagation verified: downstream branch reflects the amended commit's content.")
}

func TestSDPartialMergeRetargetsNextPRBaseToMain(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Partial Merge Base Re-Targeting ---")
	for i := 1; i <= 3; i++ {
		filename := filepath.Join(repoDir, fmt.Sprintf("filePartMerge%d.txt", i))
		if err := os.WriteFile(filename, fmt.Appendf(nil, "partial merge %d - %d", i, os.Getpid()), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("PartMerge %d", i))
	}

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var pmIndices []string
	for _, n := range []string{"1", "2", "3"} {
		found := ""
		for line := range strings.SplitSeq(out, "\n") {
			if strings.Contains(line, "PartMerge "+n) {
				found = statusIndex(t, out, line)
			}
		}
		if found == "" {
			t.Fatalf("could not find index for PartMerge %s. Status output:\n%s", n, out)
		}
		pmIndices = append(pmIndices, found)
	}

	out = runGitSD(t, repoDir, "assign", fmt.Sprintf("partmg:%s-%s", pmIndices[0], pmIndices[2]))
	fmt.Println(out)

	out = runGitSD(t, repoDir, "sync", "partmg")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	partmgPRs := extractStackPRNumbers(out, "partmg")
	if len(partmgPRs) != 3 {
		t.Fatalf("expected 3 PRs for partmg, got %d. Status output:\n%s", len(partmgPRs), out)
	}

	// Confirm the initial chain: PR 2's base should be PR 1's branch before any merge.
	base2BeforeOut := runGit(t, repoDir, "pr", "view", partmgPRs[1], "--json", "baseRefName")
	var base2Before struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(base2BeforeOut), &base2Before); err != nil {
		t.Fatalf("failed to parse partmg PR #%s base: %v", partmgPRs[1], err)
	}
	if base2Before.BaseRefName == "main" {
		t.Fatalf("expected PR #%s to be chained onto PR #%s's branch (not main) before any merge, got base %q", partmgPRs[1], partmgPRs[0], base2Before.BaseRefName)
	}

	// Simulate the bottom PR having already been finished (merged/consolidated)
	// on GitHub, without fighting this repo's self-approval restriction: closing
	// it produces the same "no longer an active branch" state git-sd's own
	// merge.go leaves lower PRs in after a squash-merge.
	runGit(t, repoDir, "pr", "close", partmgPRs[0])

	out = runGitSD(t, repoDir, "sync", "partmg")
	fmt.Println(out)

	base2AfterOut := runGit(t, repoDir, "pr", "view", partmgPRs[1], "--json", "baseRefName")
	var base2After struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(base2AfterOut), &base2After); err != nil {
		t.Fatalf("failed to parse partmg PR #%s base after re-sync: %v", partmgPRs[1], err)
	}
	if base2After.BaseRefName != "main" {
		t.Fatalf("expected PR #%s's base to retarget to 'main' after its predecessor was closed, got %q", partmgPRs[1], base2After.BaseRefName)
	}
	fmt.Println("Partial merge base re-targeting verified: PR base moved to main after predecessor PR closed.")

	out = runGitSD(t, repoDir, "rebase")
	fmt.Println(out)

	// Regression check: rebase must retire the closed PR 1 but must not
	// touch the still-open PRs 2 and 3 — a Stack Set is a living object, not
	// a one-shot construct torn down the moment any one of its PRs finishes.
	out = runGitSD(t, repoDir, "status")
	survivors := extractStackPRNumbers(out, "partmg")
	if len(survivors) != 2 {
		t.Fatalf("expected rebase to leave the 2 still-open partmg PRs tracked, got %d. Status output:\n%s", len(survivors), out)
	}
	for _, n := range survivors {
		if n == partmgPRs[0] {
			t.Fatalf("rebase should have retired closed PR #%s, but it is still tracked. Status output:\n%s", partmgPRs[0], out)
		}
	}
	fmt.Println("rebase verified: closed PR retired, still-open survivors left tracked.")
}

// TestSDMergeUptoRejectsInvalidUsage verifies the `--upto` flag's argument
// validation (rejected during parsing, before any repo/network work).
func TestSDMergeUptoRejectsInvalidUsage(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing value", []string{"merge", "someset", "--upto"}, "--upto requires an index"},
		{"non-integer", []string{"merge", "someset", "--upto", "notanum"}, "invalid --upto index"},
		{"combined with --all", []string{"merge", "--all", "--upto", "0"}, "cannot be combined with --all"},
	} {
		out, err := runGitSDErr(repoDir, tc.args...)
		if err == nil {
			t.Fatalf("[%s] expected `git sd %v` to fail, but it succeeded. Output:\n%s", tc.name, tc.args, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("[%s] expected error containing %q, got:\n%s", tc.name, tc.want, out)
		}
	}
	fmt.Println("merge --upto invalid usage correctly rejected.")
}

// TestSDMergeUptoMergesBottomPrefixLeavingUpperPRs verifies `--upto <index>`
// merges only the bottom prefix of a stack (never a cherry-picked upper PR):
// a 3-PR stack merged with `--upto 1` lands PRs 0-1 on main and leaves the PR
// at index 2 open, which then retargets to main carrying only its own commit.
func TestSDMergeUptoMergesBottomPrefixLeavingUpperPRs(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "uptoflag", "Upto Commit", 3)
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "assign", "uptoflag:0-2")
	runGitSD(t, repoDir, "status")
	runGitSD(t, repoDir, "sync")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "uptoflag") // status order: index 0, 1, 2
	if len(prs) != 3 {
		t.Fatalf("expected 3 PRs for uptoflag, got %d. Status:\n%s", len(prs), out)
	}
	topPR := prs[2] // index 2 — must NOT be merged by --upto 1

	mout, merr := runGitSDErr(repoDir, "merge", "uptoflag", "--upto", "1")
	if merr != nil {
		t.Fatalf("git sd merge uptoflag --upto 1 failed: %v\nOutput: %s", merr, mout)
	}

	// merge is GitHub-only: drop the merged prefix locally, then retarget the
	// surviving upper PR onto main.
	runGitSD(t, repoDir, "rebase")
	runGitSD(t, repoDir, "sync")

	view := runGit(t, repoDir, "pr", "view", topPR, "--json", "state,baseRefName,headRefName")
	var info struct {
		State       string `json:"state"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(view), &info); err != nil {
		t.Fatalf("failed to parse PR #%s info: %v", topPR, err)
	}
	if info.State != "OPEN" {
		t.Fatalf("--upto 1 should leave the index-2 PR #%s OPEN, but it is %s", topPR, info.State)
	}
	if info.BaseRefName != "main" {
		t.Fatalf("after merging the prefix + sync, surviving PR #%s should base on main, got %q", topPR, info.BaseRefName)
	}
	runGit(t, repoDir, "fetch", "origin")
	logOut := runGit(t, repoDir, "log", "--format=%s", "origin/main..origin/"+info.HeadRefName)
	if !strings.Contains(logOut, "Upto Commit 3") || strings.Contains(logOut, "Upto Commit 1") || strings.Contains(logOut, "Upto Commit 2") {
		t.Fatalf("surviving PR #%s branch should carry only 'Upto Commit 3'; got:\n%s", topPR, logOut)
	}
	fmt.Println("merge --upto verified: bottom prefix merged, upper PR left open and clean.")

	cout, cerr := runGitSDErr(repoDir, "rebase")
	if cerr != nil {
		t.Fatalf("rebase failed: %v\nOutput: %s", cerr, cout)
	}

	// Regression check: the surviving open PR (left by --upto) must still be
	// tracked after rebase — rebase must never retire a still-open PR just
	// because some of its stack set already merged.
	out = runGitSD(t, repoDir, "status")
	survivors := extractStackPRNumbers(out, "uptoflag")
	if len(survivors) != 1 || survivors[0] != topPR {
		t.Fatalf("expected rebase to leave surviving PR #%s tracked under uptoflag, got %v. Status output:\n%s", topPR, survivors, out)
	}
	fmt.Println("rebase verified: merged prefix retired, surviving open PR left tracked.")
}

// TestSDAssignRejectsCommitDependentOnAnotherStackSet reproduces
// GIT-SD-BUG.md: a commit whose content depends on a different, unmerged
// Stack Set's change must be refused when assigned as a NEW top-level commit
// of its own Stack Set — 'sync' only ever rebuilds a Stack Set's own commits
// onto the base branch, so such a commit could never sync cleanly (it would
// previously fail late, as a confusing cherry-pick conflict inside 'sync',
// exiting 0 anyway). 'assign' now catches this up front via
// git.SimulateChainBuild and refuses, leaving metadata untouched.
func TestSDAssignRejectsCommitDependentOnAnotherStackSet(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "depshared.txt"), []byte("line1\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "depshared.txt")
	runGit(t, repoDir, "commit", "-m", "Dep Base")

	// This commit's diff depends on "Dep Base" already being present.
	if err := os.WriteFile(filepath.Join(repoDir, "depshared.txt"), []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "depshared.txt")
	runGit(t, repoDir, "commit", "-m", "Dep Feature")

	out := runGitSD(t, repoDir, "assign", "dep-a:0")
	fmt.Println(out)

	out, err := runGitSDErr(repoDir, "assign", "dep-b:1")
	if err == nil {
		t.Fatalf("expected assigning a dependent commit to a new Stack Set to fail, but it succeeded. Output:\n%s", out)
	}
	if !strings.Contains(out, "dep-a") {
		t.Fatalf("expected the error to name the Stack Set the commit actually depends on ('dep-a'), got:\n%s", out)
	}
	fmt.Println("Cross-Stack-Set dependency correctly rejected at assign time.")

	// The rejected assignment must not have touched dep-a, and 'Dep Feature'
	// must remain unassigned (Untracked/Ready — not silently attached to
	// dep-b, which would have masked the failed validation).
	statusOut := runGitSD(t, repoDir, "status")
	var depAline, depFline string
	for line := range strings.SplitSeq(statusOut, "\n") {
		if strings.Contains(line, "Dep Base") {
			depAline = line
		}
		if strings.Contains(line, "Dep Feature") {
			depFline = line
		}
	}
	if !strings.Contains(depAline, "dep-a") {
		t.Fatalf("expected 'Dep Base' to remain assigned to dep-a, line: %s", depAline)
	}
	if strings.Contains(depFline, "dep-b") {
		t.Fatalf("expected 'Dep Feature' to remain unassigned after the rejected attempt, line: %s", depFline)
	}
	fmt.Println("Confirmed metadata untouched by the rejected assignment.")
}

// TestSDAssignAllowsChainedCommitsInSameStackSet guards against a false
// positive in the dependency check above: multiple top-level commits that
// depend on each other but are assigned TOGETHER to the SAME Stack Set (the
// everyday case, e.g. 'assign feat-a:0-2') must still succeed — 'sync'
// correctly chains a Stack Set's own top-level commits onto each other's
// rebuilt tips, not onto the bare base branch independently.
func TestSDAssignAllowsChainedCommitsInSameStackSet(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "chained.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "chained.txt")
	runGit(t, repoDir, "commit", "-m", "Chain Commit 1")

	if err := os.WriteFile(filepath.Join(repoDir, "chained.txt"), []byte("v1\nv2\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "chained.txt")
	runGit(t, repoDir, "commit", "-m", "Chain Commit 2")

	if err := os.WriteFile(filepath.Join(repoDir, "chained.txt"), []byte("v1\nv2\nv3\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "chained.txt")
	runGit(t, repoDir, "commit", "-m", "Chain Commit 3")

	out, err := runGitSDErr(repoDir, "assign", "chain-x:0-2")
	if err != nil {
		t.Fatalf("expected chained commits assigned together to the same Stack Set to succeed, got error: %v\nOutput:\n%s", err, out)
	}

	statusOut := runGitSD(t, repoDir, "status")
	for _, msg := range []string{"Chain Commit 1", "Chain Commit 2", "Chain Commit 3"} {
		var line string
		for l := range strings.SplitSeq(statusOut, "\n") {
			if strings.Contains(l, msg) {
				line = l
			}
		}
		if !strings.Contains(line, "chain-x") || !strings.Contains(line, "📦") {
			t.Fatalf("expected %q to be assigned to chain-x, line: %s", msg, line)
		}
	}
	fmt.Println("Chained commits assigned together to one Stack Set correctly succeeded.")
}

// TestSDMoveInsertsRatherThanSwaps verifies that `move <src>:<dest>` is an
// insertion, not a swap: moving a commit to an already-occupied index
// shifts that commit and everything after it later by one, rather than
// exchanging places with whatever was there.
func TestSDMoveInsertsRatherThanSwaps(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "shifttest", "ShiftTest Commit", 4)
	runGitSD(t, repoDir, "track")

	// Move commit 0 ("ShiftTest Commit 1") to index 2. With it removed, the
	// remaining commits are [2, 3, 4] (by subject); inserting at slot 2
	// gives [2, 3, 1, 4] -- commits 2 and 3 are NOT swapped with commit 1,
	// they shift left to fill the gap, then commit 1 lands after them.
	out := runGitSD(t, repoDir, "move", "0:2")
	fmt.Println(out)

	gotOrder := runGit(t, repoDir, "log", "--format=%s", "--reverse", "origin/main..HEAD")
	wantOrder := []string{"ShiftTest Commit 2", "ShiftTest Commit 3", "ShiftTest Commit 1", "ShiftTest Commit 4"}
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(gotOrder), "\n") {
		got = append(got, line)
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("expected %d commits after move, got %d:\n%v", len(wantOrder), len(got), got)
	}
	for i, want := range wantOrder {
		if got[i] != want {
			t.Fatalf("move 0:2 should insert (not swap): expected order %v, got %v", wantOrder, got)
		}
	}
	fmt.Println("move verified as insertion (shifts others), not a swap.")
}

func TestSDMoveDetectsConflictReorderingDependentCommits(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Move Conflict Detection
	//
	// move's RebuildCommitChain currently rebuilds the reordered chain by
	// reusing each commit's own full tree snapshot (commit-tree), never
	// recomputing diffs. Reordering two commits where the later one's
	// content depends on the earlier one doesn't conflict under that scheme —
	// it just silently discards the dependent commit's change from the final
	// tree. This test is expected to FAIL against that implementation
	// (demonstrating the bug); once RebuildCommitChain uses a real
	// cherry-pick sequence instead, it should PASS because the reorder
	// correctly surfaces a genuine conflict rather than corrupting content.
	fmt.Println("--- Running Scenario: Move Conflict Detection ---")
	if err := os.WriteFile(filepath.Join(repoDir, "moveconflict.txt"), []byte("line1\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "moveconflict.txt")
	runGit(t, repoDir, "commit", "-m", "Move Conflict Base")

	if err := os.WriteFile(filepath.Join(repoDir, "moveconflict.txt"), []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", "moveconflict.txt")
	runGit(t, repoDir, "commit", "-m", "Move Conflict Feature")

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var idxMoveBase, idxMoveFeature string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Move Conflict Base") {
			idxMoveBase = statusIndex(t, out, line)
		}
		if strings.Contains(line, "Move Conflict Feature") {
			idxMoveFeature = statusIndex(t, out, line)
		}
	}
	if idxMoveBase == "" || idxMoveFeature == "" {
		t.Fatalf("could not find indices for Move Conflict Base/Feature. Status output:\n%s", out)
	}

	// Swap them: move Feature to Base's position, so Feature would need to
	// apply before Base exists.
	preMoveHead := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	moveOut, moveErr := runGitSDErr(repoDir, "move", fmt.Sprintf("%s:%s", idxMoveFeature, idxMoveBase))
	if moveErr == nil {
		t.Fatalf("expected moving Feature before Base to fail with a real conflict (Feature's change depends on Base's content already existing), but it succeeded — likely silently discarding Feature's change instead of detecting the conflict. Output:\n%s", moveOut)
	}
	if !strings.Contains(moveOut, "onflict") {
		t.Fatalf("expected move failure to mention a conflict, got: %s", moveOut)
	}
	fmt.Println("Move conflict handling verified: reordering commits whose content depends on each other correctly surfaces a real conflict instead of silently discarding a change.")

	// A plain 'git rebase --abort' must restore the pre-move state, and status
	// must show both commits again.
	if _, err := runGitErr(repoDir, "rebase", "--abort"); err != nil {
		t.Fatalf("git rebase --abort failed after a conflicting move: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD")); got != preMoveHead {
		t.Fatalf("HEAD after rebase --abort = %s, want %s", got, preMoveHead)
	}
	out = runGitSD(t, repoDir, "status")
	if !strings.Contains(out, "Move Conflict Base") || !strings.Contains(out, "Move Conflict Feature") {
		t.Fatalf("status after rebase --abort is missing commits:\n%s", out)
	}
}

func TestSDAllFlagAppliesToEveryStackSet(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: --all variants (sync/merge)
	fmt.Println("--- Running Scenario: --all variants ---")
	for i := 6; i <= 7; i++ {
		filename := filepath.Join(repoDir, fmt.Sprintf("file%d.txt", i))
		content := fmt.Sprintf("content %d - %d", i, os.Getpid())
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatalf("%v", err)
		}
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", fmt.Sprintf("Commit %d", i))
	}

	runGitSD(t, repoDir, "track")
	out := runGitSD(t, repoDir, "status")
	var idx6, idx7 string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Commit 6") {
			idx6 = statusIndex(t, out, line)
		}
		if strings.Contains(line, "Commit 7") {
			idx7 = statusIndex(t, out, line)
		}
	}
	if idx6 == "" || idx7 == "" {
		t.Fatalf("could not find indices for Commit 6/Commit 7. Status output:\n%s", out)
	}

	out = runGitSD(t, repoDir, "assign", fmt.Sprintf("all-x:%s", idx6), fmt.Sprintf("all-y:%s", idx7))
	fmt.Println(out)

	out = runGitSD(t, repoDir, "sync", "--all")
	fmt.Println(out)

	out = runGitSD(t, repoDir, "status")
	prsX := extractStackPRNumbers(out, "all-x")
	prsY := extractStackPRNumbers(out, "all-y")
	if len(prsX) == 0 || len(prsY) == 0 {
		t.Fatalf("sync --all did not create PRs for both stack sets. Status output:\n%s", out)
	}
	fmt.Println("sync --all verified: both stack sets got PRs.")

	// merge --all now succeeds for both sets with no approval needed:
	// approval/CI enforcement is delegated entirely to GitHub branch
	// protection, and this repo has none configured, so an unapproved merge
	// isn't blocked (unlike before this migration, when git-sd's own
	// required-approvals safeguard always rejected self-approval on this
	// repo). Capture the PR numbers before merging — once `rebase` drops
	// the merged commits from the local branch, git-sd's own metadata won't
	// have anything left to report on afterward.
	mergeOut, mergeErr := runGitSDErr(repoDir, "merge", "--all")
	if mergeErr != nil {
		t.Fatalf("merge --all failed: %v\nOutput: %s", mergeErr, mergeOut)
	}
	for _, n := range append(append([]string{}, prsX...), prsY...) {
		if state := prState(t, repoDir, n); state != "MERGED" {
			t.Fatalf("expected PR #%s to be MERGED after merge --all, got %q", n, state)
		}
	}
	fmt.Println("merge --all verified: both stack sets merged independently.")

	// merge is GitHub-only: drop the merged commits locally before syncing.
	runGitSD(t, repoDir, "rebase")
	out = runGitSD(t, repoDir, "sync", "--all")
	fmt.Println(out)
	if got := deletedBranchCount(t, out); got != len(prsX)+len(prsY) {
		t.Fatalf("expected sync --all to delete every merged branch (%d), it reported %d. Output:\n%s", len(prsX)+len(prsY), got, out)
	}

	out = runGitSD(t, repoDir, "status")
	if strings.Contains(out, "all-x") || strings.Contains(out, "all-y") {
		t.Fatalf("stack sets still present after merge --all + sync --all. Status output:\n%s", out)
	}
	fmt.Println("sync --all verified: all merged branches deleted, all stack sets gone.")
}

// TestSDRebasePreservesUnmergedSurvivorAfterPartialMerge is a direct
// regression test for B-018: given a 3-PR stack whose bottom two PRs have
// already finished (closed), `rebase` must retire only those two — the
// still-open top PR must keep its Stack Set membership and branch, not be
// silently detached from the stack.
func TestSDRebasePreservesUnmergedSurvivorAfterPartialMerge(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "cleanupsurv", "CleanupSurv Commit", 3)
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "assign", "cleanupsurv:0-2")
	runGitSD(t, repoDir, "sync", "cleanupsurv")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "cleanupsurv")
	if len(prs) != 3 {
		t.Fatalf("expected 3 PRs for cleanupsurv, got %d. Status:\n%s", len(prs), out)
	}

	runGit(t, repoDir, "pr", "close", prs[0])
	runGit(t, repoDir, "pr", "close", prs[1])

	cout, cerr := runGitSDErr(repoDir, "rebase")
	if cerr != nil {
		t.Fatalf("rebase failed: %v\nOutput: %s", cerr, cout)
	}

	out = runGitSD(t, repoDir, "status")
	survivors := extractStackPRNumbers(out, "cleanupsurv")
	if len(survivors) != 1 || survivors[0] != prs[2] {
		t.Fatalf("expected rebase to leave only the surviving open PR #%s tracked under cleanupsurv, got %v. Status output:\n%s", prs[2], survivors, out)
	}
	fmt.Println("rebase regression verified: unmerged survivor keeps its Stack Set membership.")
}

// TestSDLivingStackAddsPRAfterFrontMerged verifies the workflow this bug
// blocked: a Stack Set is not a one-shot construct created once and merged
// all at once. After merging PRs off the front and running rebase, a new
// commit can still be assigned to the very same Stack Set and synced onto
// its back, chaining behind the surviving PR rather than starting a fresh,
// disconnected set.
func TestSDLivingStackAddsPRAfterFrontMerged(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "livingstack", "LivingStack Commit", 2)
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "assign", "livingstack:0-1")
	runGitSD(t, repoDir, "sync", "livingstack")

	out := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(out, "livingstack")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs for livingstack, got %d. Status:\n%s", len(prs), out)
	}

	// Finish the bottom PR (closed), retire it, and retarget the survivor.
	runGit(t, repoDir, "pr", "close", prs[0])
	runGitSD(t, repoDir, "rebase")
	runGitSD(t, repoDir, "sync", "livingstack")

	out = runGitSD(t, repoDir, "status")
	if survivors := extractStackPRNumbers(out, "livingstack"); len(survivors) != 1 || survivors[0] != prs[1] {
		t.Fatalf("expected only surviving PR #%s tracked after rebase, got %v. Status:\n%s", prs[1], survivors, out)
	}

	// Add a new commit onto the back of the still-living Stack Set.
	filename := filepath.Join(repoDir, "livingstack3.txt")
	if err := os.WriteFile(filename, fmt.Appendf(nil, "livingstack 3 - %d", os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, repoDir, "add", filename)
	runGit(t, repoDir, "commit", "-m", "LivingStack Commit 3")

	runGitSD(t, repoDir, "track")
	out = runGitSD(t, repoDir, "status")
	var newIdx string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "LivingStack Commit 3") {
			newIdx = statusIndex(t, out, line)
		}
	}
	if newIdx == "" {
		t.Fatalf("could not find index for new commit. Status output:\n%s", out)
	}

	runGitSD(t, repoDir, "assign", fmt.Sprintf("livingstack:%s", newIdx))
	runGitSD(t, repoDir, "sync", "livingstack")

	out = runGitSD(t, repoDir, "status")
	finalPRs := extractStackPRNumbers(out, "livingstack")
	if len(finalPRs) != 2 {
		t.Fatalf("expected the living stack to have 2 PRs (surviving + newly added), got %d. Status:\n%s", len(finalPRs), out)
	}
	if finalPRs[0] != prs[1] {
		t.Fatalf("expected surviving PR #%s to still be the bottom of the living stack, got %v", prs[1], finalPRs)
	}
	fmt.Println("living-stack workflow verified: new commit chained onto the back of a partially-merged, still-tracked Stack Set.")
}

// TestSDResyncWithoutChangesDoesNotRepush: sync rebuilds each PR's pushed
// commits deterministically, so re-syncing unchanged PRs — even after every
// local commit was rewritten with a new committer date, as move/rebase do —
// leaves their remote branches untouched (no force-push churn on GitHub).
// Changing only the top commit re-pushes only the top PR.
func TestSDResyncWithoutChangesDoesNotRepush(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "norepush", "NoRepush Commit", 2)
	runGitSD(t, repoDir, "assign", "norepush:0-1")
	runGitSD(t, repoDir, "sync", "norepush")
	prs := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "norepush")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %v", prs)
	}
	branches := []string{prHeadRef(t, repoDir, prs[0]), prHeadRef(t, repoDir, prs[1])}
	remoteSHA := func(branch string) string {
		fields := strings.Fields(runGit(t, repoDir, "ls-remote", "--heads", "origin", branch))
		if len(fields) == 0 {
			t.Fatalf("branch %s missing from the remote", branch)
		}
		return fields[0]
	}
	before := []string{remoteSHA(branches[0]), remoteSHA(branches[1])}

	// Rewrite both local commits with only a new committer date.
	headBefore := runGit(t, repoDir, "rev-parse", "HEAD")
	rewrite := exec.Command("git", "rebase", "-q", "--force-rebase", "HEAD~2")
	rewrite.Dir = repoDir
	rewrite.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2030-01-01T00:00:00Z")
	if out, err := rewrite.CombinedOutput(); err != nil {
		t.Fatalf("rewriting local commits failed: %v\n%s", err, out)
	}
	if runGit(t, repoDir, "rev-parse", "HEAD") == headBefore {
		t.Fatal("test setup: local commits were not rewritten")
	}

	runGitSD(t, repoDir, "sync", "norepush")
	for i, b := range branches {
		if got := remoteSHA(b); got != before[i] {
			t.Fatalf("re-sync with no content change re-pushed %s: %s -> %s", b, before[i], got)
		}
	}

	// Change only the top PR's content: only its branch moves.
	if err := os.WriteFile(filepath.Join(repoDir, "norepush2.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "commit", "-q", "-a", "--amend", "--no-edit")
	runGitSD(t, repoDir, "sync", "norepush")
	if got := remoteSHA(branches[0]); got != before[0] {
		t.Fatalf("changing only the top PR re-pushed the bottom one: %s -> %s", before[0], got)
	}
	if got := remoteSHA(branches[1]); got == before[1] {
		t.Fatalf("changing the top PR's content did not re-push it")
	}
	fmt.Println("No-repush verified: unchanged PRs keep their remote SHAs; a changed PR is re-pushed alone.")
}

// TestSDSyncReportsOnlyWhatChanged verifies sync's summary: it lists the PRs it
// created or updated (with their URLs), counts the unchanged ones, records a
// fingerprint so it stops re-editing PRs that haven't changed, and reports an
// update only when a PR's title/body/base or branch really changed.
func TestSDSyncReportsOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Sync Reports Only What Changed ---")
	makeCommits(t, repoDir, "syncsum", "SyncSum Commit", 2)
	runGitSD(t, repoDir, "assign", "syncsum:0-1")

	prURL := func(statusOut, number string) string {
		for _, line := range strings.Split(statusOut, "\n") {
			if strings.Contains(line, "/pull/"+number) {
				fields := strings.Fields(line)
				return fields[len(fields)-1]
			}
		}
		return ""
	}

	// 1. The first sync creates both PRs and says so, with the same URLs status shows.
	out := runGitSD(t, repoDir, "sync", "syncsum")
	statusOut := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(statusOut, "syncsum")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs after sync, got %v. Status:\n%s", prs, statusOut)
	}
	for i, n := range prs {
		url := prURL(statusOut, n)
		want := fmt.Sprintf("Created PR #%s  [syncsum]  SyncSum Commit %d  %s", n, i+1, url)
		if url == "" || !strings.Contains(out, want) {
			t.Fatalf("sync output is missing %q. Output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Updated PR") || strings.Contains(out, "unchanged") {
		t.Fatalf("a first sync should only create PRs. Output:\n%s", out)
	}

	// 2. Each PR's title/body/base fingerprint was recorded, so later syncs can skip the edit.
	if got := strings.Count(string(metadataBytes(t, repoDir)), `"last_synced"`); got != 2 {
		t.Fatalf("expected last_synced recorded for both PRs, found %d", got)
	}

	// 3. Syncing again with nothing changed reports only the unchanged count.
	out = runGitSD(t, repoDir, "sync", "syncsum")
	if !strings.Contains(out, "2 PRs unchanged.") || strings.Contains(out, "Created PR") || strings.Contains(out, "Updated PR") {
		t.Fatalf("an unchanged re-sync should report only '2 PRs unchanged.'. Output:\n%s", out)
	}

	// 4. Renaming the top commit updates only its PR.
	msg := strings.TrimSpace(runGit(t, repoDir, "log", "-1", "--format=%B"))
	runGit(t, repoDir, "commit", "--amend", "-m", strings.Replace(msg, "SyncSum Commit 2", "SyncSum Commit 2 renamed", 1))
	out = runGitSD(t, repoDir, "sync", "syncsum")
	want := fmt.Sprintf("Updated PR #%s  [syncsum]  SyncSum Commit 2 renamed  %s", prs[1], prURL(statusOut, prs[1]))
	if !strings.Contains(out, want) || !strings.Contains(out, "1 PR unchanged.") || strings.Contains(out, "Created PR") {
		t.Fatalf("expected only PR #%s reported as updated plus '1 PR unchanged.'. Output:\n%s", prs[1], out)
	}
	fmt.Println("Sync summary verified: created, unchanged, and updated PRs are each reported once and correctly.")
}
