package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/efarrer/git-sd/internal/ui"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dotEnvValues parses a .env file at the repo root (KEY=VALUE per line,
// blank lines and lines starting with "#" ignored) into a map — see
// .env.example for the variables the suite reads this way. The module root
// is located via `go env GOMOD` so this works regardless of which directory
// `go test` is invoked from. A missing .env file is not an error: real
// environment variables work exactly as they did before this existed.
func dotEnvValues() (map[string]string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return nil, fmt.Errorf("could not locate module root: %w", err)
	}
	modFile := strings.TrimSpace(string(out))
	if modFile == "" || modFile == os.DevNull {
		return nil, nil
	}

	data, err := os.ReadFile(filepath.Join(filepath.Dir(modFile), ".env"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	values := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf(".env:%d: invalid line (expected KEY=VALUE): %q", i+1, line)
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values, nil
}

// getTestEnv returns the real environment variable named key if set and
// non-empty, else falls back to dotEnv's value for key (empty if absent from
// both) — a real env var always wins over the .env file.
func getTestEnv(dotEnv map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dotEnv[key]
}

// ghEnv returns the environment for a gh/git subprocess that should act as
// the given identity's token: GH_TOKEN set on cmd.Env for just that one
// exec.Command, never os.Setenv/t.Setenv on the test binary's own process —
// so any number of t.Parallel() tests can do this concurrently without
// cross-contaminating each other. An empty token falls back to the ambient
// `gh auth login` session (today's behavior, unchanged).
func ghEnv(token string) []string {
	if token == "" {
		return os.Environ()
	}
	return append(os.Environ(), "GH_TOKEN="+token)
}

// gitSDBranchPattern matches git-sd's branch-naming scheme: a slug of the
// commit subject plus "-" and the 5-character git-sd-id suffix (always
// starting with 's', e.g. "-s3f2a"), or just the bare git-sd-id if the
// subject slugified to empty. Sanitization only ever touches branches
// matching this, so it can never disturb other tools (e.g. "spr/...")
// sharing the same sacrificial repo.
var gitSDBranchPattern = regexp.MustCompile(`(^|-)s[a-z0-9]{4}$`)

func isGitSDBranch(ref string) bool {
	return gitSDBranchPattern.MatchString(ref)
}

// statusColumnStarts returns the display column where each column of a
// `git sd status` table starts, from its header line. The first column (Icon)
// has a blank header and starts at 0.
func statusColumnStarts(header string) map[string]int {
	starts := map[string]int{"Icon": 0}
	from := 0
	for _, name := range []string{"Status", "Index", "Stack Set", "git-sd-id", "Hash", "Message", "PR"} {
		i := strings.Index(header[from:], name)
		if i < 0 {
			continue
		}
		from += i
		starts[name] = ui.DisplayWidth(header[:from])
		from += len(name)
	}
	return starts
}

// statusCell returns one column's cell of a status table row, using the
// header's column positions (cells can hold spaces and emoji, so splitting on
// whitespace is not reliable).
func statusCell(t *testing.T, out, row, column string) string {
	t.Helper()
	var header string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Index") && strings.Contains(line, "Message") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("no status table header in output:\n%s", out)
	}
	starts := statusColumnStarts(header)
	start, ok := starts[column]
	if !ok {
		t.Fatalf("column %q not in header %q", column, header)
	}
	end := -1
	for _, s := range starts {
		if s > start && (end < 0 || s < end) {
			end = s
		}
	}
	var cell strings.Builder
	w := 0
	for _, r := range row {
		rw := ui.DisplayWidth(string(r))
		if w >= start && (end < 0 || w < end) {
			cell.WriteRune(r)
		}
		w += rw
	}
	return strings.TrimSpace(cell.String())
}

// statusIndex returns the Index cell of a row of `git sd status` output.
func statusIndex(t *testing.T, out, row string) string {
	t.Helper()
	return statusCell(t, out, row, "Index")
}

// extractStackPRNumbers scans a `git sd status` table and returns the PR
// numbers (as strings) for every row belonging to the given stack set.
func extractStackPRNumbers(statusOut, setName string) []string {
	var numbers []string
	for line := range strings.SplitSeq(statusOut, "\n") {
		if strings.Contains(line, setName) && strings.Contains(line, "https://github.com/") {
			parts := strings.Fields(line)
			url := parts[len(parts)-1]
			urlParts := strings.Split(url, "/")
			numbers = append(numbers, urlParts[len(urlParts)-1])
		}
	}
	return numbers
}

type prRef struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
}

// gitSDOpenPRs returns open PRs whose head branch belongs to git-sd (per gitSDBranchPrefixes).
func gitSDOpenPRs(dir string) ([]prRef, error) {
	out, err := runGitErr(dir, "pr", "list", "--state", "open", "--json", "number,headRefName", "--limit", "200")
	if err != nil {
		return nil, fmt.Errorf("failed to list open PRs: %w", err)
	}
	var prs []prRef
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("failed to parse PR list: %w", err)
	}
	var owned []prRef
	for _, pr := range prs {
		if isGitSDBranch(pr.HeadRefName) {
			owned = append(owned, pr)
		}
	}
	return owned, nil
}

// gitSDRemoteBranches returns remote branch names belonging to git-sd (per gitSDBranchPrefixes).
func gitSDRemoteBranches(dir string) ([]string, error) {
	branchOut, err := runGitErr(dir, "ls-remote", "--heads", "origin")
	if err != nil {
		return nil, fmt.Errorf("failed to list remote branches: %w", err)
	}
	var owned []string
	for line := range strings.SplitSeq(branchOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := strings.TrimPrefix(fields[1], "refs/heads/")
		if isGitSDBranch(ref) {
			owned = append(owned, ref)
		}
	}
	return owned, nil
}

// auxTestBranches lists non-PR branches that base-branch test scenarios
// create directly on the sacrificial repo's remote (not through git-sd's own
// branch-prefix mechanism, so isGitSDBranch doesn't cover them). sanitizeRepo
// deletes these unconditionally if they exist, so a crash between creating
// and tearing one down in a previous run can't leave it dangling forever.
var auxTestBranches = []string{"sd-test-alt-base", "sd-test-rebase-conflict-base"}

// leftoverAuxBranches returns which of auxTestBranches currently exist on the remote.
func leftoverAuxBranches(dir string) ([]string, error) {
	branchOut, err := runGitErr(dir, "ls-remote", "--heads", "origin")
	if err != nil {
		return nil, fmt.Errorf("failed to list remote branches: %w", err)
	}
	var present []string
	for line := range strings.SplitSeq(branchOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := strings.TrimPrefix(fields[1], "refs/heads/")
		if slices.Contains(auxTestBranches, ref) {
			present = append(present, ref)
		}
	}
	return present, nil
}

// sanitizeRepo brings the sacrificial repo to a known-clean state before any
// scenario runs, so leftovers from a previous failed/interrupted run (open
// PRs, dangling branches) can't cause false failures or false passes in this run.
// It re-queries the repo after attempting cleanup and treats anything still
// present as fatal, rather than trusting that the close/delete calls worked.
func sanitizeRepo(dir string) error {
	fmt.Println("Sanitizing sacrificial repo (closing leftover git-sd PRs/branches from prior runs)...")

	prs, err := gitSDOpenPRs(dir)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		fmt.Printf("  Closing leftover PR #%d (%s)...\n", pr.Number, pr.HeadRefName)
		if out, err := runGitErr(dir, "pr", "close", fmt.Sprintf("%d", pr.Number), "--delete-branch"); err != nil {
			fmt.Printf("  Warning: failed to close leftover PR #%d: %v\nOutput: %s\n", pr.Number, err, out)
		}
	}

	branches, err := gitSDRemoteBranches(dir)
	if err != nil {
		return err
	}
	for _, ref := range branches {
		fmt.Printf("  Deleting orphaned branch %s...\n", ref)
		if out, err := runGitErr(dir, "push", "origin", "--delete", ref); err != nil {
			fmt.Printf("  Warning: failed to delete orphaned branch %s: %v\nOutput: %s\n", ref, err, out)
		}
	}

	auxBranches, err := leftoverAuxBranches(dir)
	if err != nil {
		return err
	}
	for _, ref := range auxBranches {
		fmt.Printf("  Deleting leftover auxiliary branch %s...\n", ref)
		if out, err := runGitErr(dir, "push", "origin", "--delete", ref); err != nil {
			fmt.Printf("  Warning: failed to delete leftover auxiliary branch %s: %v\nOutput: %s\n", ref, err, out)
		}
	}

	// Verify: don't trust that the close/delete calls above actually worked
	// (transient gh/API failures, permissions, etc.) — re-query live state and
	// fail loudly if anything git-sd-owned is still present, rather than silently
	// running scenarios against a repo we only think is clean.
	leftoverPRs, err := gitSDOpenPRs(dir)
	if err != nil {
		return fmt.Errorf("failed to verify PR cleanup: %w", err)
	}
	leftoverBranches, err := gitSDRemoteBranches(dir)
	if err != nil {
		return fmt.Errorf("failed to verify branch cleanup: %w", err)
	}
	leftoverAux, err := leftoverAuxBranches(dir)
	if err != nil {
		return fmt.Errorf("failed to verify auxiliary branch cleanup: %w", err)
	}
	if len(leftoverPRs) > 0 || len(leftoverBranches) > 0 || len(leftoverAux) > 0 {
		var details []string
		for _, pr := range leftoverPRs {
			details = append(details, fmt.Sprintf("PR #%d (%s)", pr.Number, pr.HeadRefName))
		}
		details = append(details, leftoverBranches...)
		details = append(details, leftoverAux...)
		return fmt.Errorf("repo still not clean after sanitization; %d item(s) remain: %s", len(details), strings.Join(details, "; "))
	}

	fmt.Println("Sanitization complete; repo confirmed clean.")
	return nil
}

// newTestRepo creates a fresh, isolated clone of the sacrificial repo for a
// single parallel test. Giving each test its own clone makes every global
// mutation (renaming the remote, sd.* config, sync/merge --all) clone-local
// and harmless, so scenarios can run concurrently. The clone borrows objects
// from testRepo's local mirror (repoMirror, built once in TestMain) via
// --reference, so it doesn't re-fetch the object graph from GitHub every
// time — its own .git config, remotes, and refs are still fully independent,
// same as before. The clone lives in t.TempDir() (auto-removed when the test
// finishes); the single global sanitize in TestMain sweeps any
// crash-leftovers off the shared remote before the run.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cloneCmd := exec.Command("gh", "repo", "clone", testRepo, dir, "--", "--reference", repoMirror)
	cloneCmd.Env = ghEnv(authorToken)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to clone test repo %q: %v\n%s", testRepo, err, string(out))
	}
	if _, err := os.Stat(filepath.Join(dir, ".git-sd-test-marker")); os.IsNotExist(err) {
		t.Fatalf("safety marker file not found in cloned test repo %s; aborting to prevent data loss", dir)
	}
	runGit(t, dir, "config", "user.name", "efarrer")
	runGit(t, dir, "config", "user.email", "efarrer@users.noreply.github.com")
	return dir
}

// prDetails holds the fields prInfo fetches in a single gh call.
type prDetails struct {
	HeadRefName    string `json:"headRefName"`
	ReviewDecision string `json:"reviewDecision"`
	BaseRefName    string `json:"baseRefName"`
	State          string `json:"state"`
}

// prInfo returns a PR's head ref, review decision, base ref, and state in a
// single `gh pr view` call, for callers that need more than one of these
// fields for the same PR back-to-back — see prHeadRef/prReviewDecision/
// prBaseRef/prState below for single-field lookups.
func prInfo(t *testing.T, dir, prNumber string) prDetails {
	t.Helper()
	out := runGit(t, dir, "pr", "view", prNumber, "--json", "headRefName,reviewDecision,baseRefName,state")
	var info prDetails
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("failed to parse PR #%s info: %v", prNumber, err)
	}
	return info
}

// prHeadRef returns a PR's head branch name.
func prHeadRef(t *testing.T, dir, prNumber string) string {
	t.Helper()
	out := runGit(t, dir, "pr", "view", prNumber, "--json", "headRefName")
	var info struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("failed to parse PR #%s head ref: %v", prNumber, err)
	}
	return info.HeadRefName
}

// prReviewDecision returns a PR's current reviewDecision (e.g. "APPROVED",
// "REVIEW_REQUIRED", or "" if none).
func prReviewDecision(t *testing.T, dir, prNumber string) string {
	t.Helper()
	out := runGit(t, dir, "pr", "view", prNumber, "--json", "reviewDecision")
	var info struct {
		ReviewDecision string `json:"reviewDecision"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("failed to parse PR #%s review decision: %v", prNumber, err)
	}
	return info.ReviewDecision
}

// prBaseRef returns a PR's current base branch name.
func prBaseRef(t *testing.T, dir, prNumber string) string {
	t.Helper()
	out := runGit(t, dir, "pr", "view", prNumber, "--json", "baseRefName")
	var info struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("failed to parse PR #%s base ref: %v", prNumber, err)
	}
	return info.BaseRefName
}

// prState returns a PR's current state (OPEN, MERGED, or CLOSED).
func prState(t *testing.T, dir, prNumber string) string {
	t.Helper()
	out := runGit(t, dir, "pr", "view", prNumber, "--json", "state")
	var info struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("failed to parse PR #%s state: %v", prNumber, err)
	}
	return info.State
}

// baseBranchScenario parameterizes a single sd.base-branch regression check:
// set (or unset) sd.base-branch, commit one file, then run the tool's full
// status/assign/sync/merge/rebase cycle, asserting the resulting PR's base
// matches wantBaseRef. Shared by the Default/Alternate/Renamed-Remote
// scenarios so they don't each repeat the same ~60 lines.
type baseBranchScenario struct {
	label       string // for log messages, e.g. "Default Base Branch"
	stackSet    string // stack set name; must be unique across scenarios
	fileName    string // file to create for the commit
	commitMsg   string // commit message
	configValue string // sd.base-branch value to set; "" means unset it
	wantBaseRef string // expected PR baseRefName after sync
}

// runBaseBranchScenario runs the shared base-branch cycle against dir (the
// primary checkout, or a worktree for the worktree scenario), using the
// package-level gitSDPath.
func runBaseBranchScenario(t *testing.T, dir string, s baseBranchScenario) {
	t.Helper()
	fmt.Printf("--- Running Scenario: %s ---\n", s.label)

	if s.configValue == "" {
		runGitErr(dir, "config", "--unset", "sd.base-branch") // best-effort; fine if already unset
	} else {
		runGit(t, dir, "config", "sd.base-branch", s.configValue)
	}

	file := filepath.Join(dir, s.fileName)
	// Include the PID so the content differs from any same-named file a prior
	// run already merged onto the base branch — otherwise `git commit` fails
	// with "nothing to commit".
	if err := os.WriteFile(file, fmt.Appendf(nil, "%s content %d", s.commitMsg, os.Getpid()), 0644); err != nil {
		t.Fatalf("%v", err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-m", s.commitMsg)

	out := runGitSD(t, dir, "status")
	if !strings.Contains(out, s.commitMsg) {
		t.Fatalf("[%s] status did not show the new commit. Status output:\n%s", s.label, out)
	}
	fmt.Printf("[%s] merge-base resolved correctly.\n", s.label)

	out = runGitSD(t, dir, "assign", fmt.Sprintf("%s:0", s.stackSet))
	fmt.Println(out)

	out = runGitSD(t, dir, "sync")
	fmt.Println(out)

	out = runGitSD(t, dir, "status")
	prs := extractStackPRNumbers(out, s.stackSet)
	if len(prs) == 0 {
		t.Fatalf("[%s] sync did not create a PR. Status output:\n%s", s.label, out)
	}

	viewOut := runGit(t, dir, "pr", "view", prs[0], "--json", "baseRefName")
	var info struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(viewOut), &info); err != nil {
		t.Fatalf("[%s] failed to parse PR #%s info: %v", s.label, prs[0], err)
	}
	if info.BaseRefName != s.wantBaseRef {
		t.Fatalf("[%s] regression: expected PR #%s to base on %q, got %q", s.label, prs[0], s.wantBaseRef, info.BaseRefName)
	}
	fmt.Printf("[%s] PR #%s correctly bases on %q.\n", s.label, prs[0], s.wantBaseRef)

	// The merge must succeed deterministically: the repo is private with no
	// branch protection, so approval isn't required (self-approval is best-effort
	// and legitimately fails), and MergePR now waits out the transient
	// mergeability race. Assert the PR actually reaches MERGED.
	runGitErr(dir, "pr", "review", prs[0], "--approve") // best-effort; own PR can't be self-approved
	mergeOut, mergeErr := runGitSDErr(dir, "merge", s.stackSet)
	if mergeErr != nil {
		t.Fatalf("[%s] git sd merge failed: %v\nOutput: %s", s.label, mergeErr, mergeOut)
	}
	fmt.Println(mergeOut)

	mergedView := runGit(t, dir, "pr", "view", prs[0], "--json", "state")
	var mergedInfo struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(mergedView), &mergedInfo); err != nil {
		t.Fatalf("[%s] failed to parse merged PR #%s info: %v", s.label, prs[0], err)
	}
	if mergedInfo.State != "MERGED" {
		t.Fatalf("[%s] expected PR #%s to be MERGED, got %q", s.label, prs[0], mergedInfo.State)
	}
	fmt.Printf("[%s] PR #%s merged into %q.\n", s.label, prs[0], s.wantBaseRef)

	out = runGitSD(t, dir, "rebase")
	fmt.Println(out)
	out = runGitSD(t, dir, "sync", s.stackSet)
	fmt.Println(out)
}

// runAsErr runs git (or gh when args[0] == "pr") in dir, authenticated as
// token's identity (see ghEnv), and returns stdout plus any error.
func runAsErr(token, dir string, args ...string) (string, error) {
	binary := "git"
	if args[0] == "pr" {
		binary = "gh"
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = ghEnv(token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("git %v failed: %w\nStderr: %s", args, err, stderr.String())
	}
	return stdout.String(), nil
}

// runGitErr runs git (or gh when args[0] == "pr") in dir as the author
// identity and returns stdout plus any error. Used by setup/teardown helpers
// that can't take a *testing.T and by negative tests that assert a command
// fails.
func runGitErr(dir string, args ...string) (string, error) {
	return runAsErr(authorToken, dir, args...)
}

// approveAsReviewer runs `gh pr review <prNumber> --approve` authenticated as
// the reviewer identity (GIT_SD_TEST_REVIEWER_TOKEN) — a genuinely different
// GitHub account from the author, since GitHub forbids self-approval. Returns
// an error (rather than attempting anything) if the reviewer token isn't
// set, so callers can turn that into a clean t.Skip.
func approveAsReviewer(dir, prNumber string) (string, error) {
	if reviewerToken == "" {
		return "", fmt.Errorf("GIT_SD_TEST_REVIEWER_TOKEN not set")
	}
	return runAsErr(reviewerToken, dir, "pr", "review", prNumber, "--approve")
}

// runGit runs git (or gh when args[0] == "pr") in dir, failing the test on
// error, and returns stdout.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitErr(dir, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// runGitSDErr runs git-sd (the built binary at gitSDPath) in dir, as the
// author identity, and returns combined output plus any error. Used by
// negative tests that assert a command fails, or where the failure is
// tolerated. There is no more GIT_SD_SKIP_SAFEGUARDS mode to select —
// approval/CI enforcement is delegated entirely to GitHub branch protection
// now, not checked by git-sd itself, so a single run mode covers every case.
// git-sd's own internal gh/git subprocess calls need no changes to honor
// this — they already just inherit whatever environment this process was
// launched with.
func runGitSDErr(dir string, args ...string) (string, error) {
	cmd := exec.Command(gitSDPath, args...)
	cmd.Dir = dir
	cmd.Env = ghEnv(authorToken)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("git-sd %v failed: %w", args, err)
	}
	return stdout.String(), nil
}

// runGitSD runs git-sd in dir, failing the test on error, and returns stdout.
func runGitSD(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitSDErr(dir, args...)
	if err != nil {
		t.Fatalf("%v\nOutput:\n%s", err, out)
	}
	return out
}

// deletedBranchCount returns n from sync's "Deleted <n> finished branch(es)."
// line, or 0 if there is none.
func deletedBranchCount(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^Deleted (\d+) finished branch(?:es)?\.$`).FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// pendingBranchDeletions returns the branches queued in the clone's metadata
// for deletion by the next sync, for the given Stack Set.
func pendingBranchDeletions(t *testing.T, repoDir, stackSet string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoDir, ".git", "sd", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Pending []struct {
			Branch   string `json:"branch"`
			StackSet string `json:"stack_set"`
		} `json:"pending_branch_deletions"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	var branches []string
	for _, p := range meta.Pending {
		if p.StackSet == stackSet {
			branches = append(branches, p.Branch)
		}
	}
	return branches
}
