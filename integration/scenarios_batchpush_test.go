package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// batchPushEnv is a local repo with a bare origin and a fake gh, so `sync`'s
// pushing and PR sequencing can be tested without GitHub: pushes are counted
// by a pre-receive hook (it runs once per push that updates something) and
// every gh call is logged.
type batchPushEnv struct {
	t       *testing.T
	root    string
	remote  string
	repo    string
	ghLog   string
	pushLog string
	pathDir string
}

func newBatchPushEnv(t *testing.T) *batchPushEnv {
	t.Helper()
	e := &batchPushEnv{t: t, root: t.TempDir()}
	e.remote = filepath.Join(e.root, "origin.git")
	e.repo = filepath.Join(e.root, "repo")
	e.ghLog = filepath.Join(e.root, "gh.log")
	e.pushLog = filepath.Join(e.root, "pushes.log")
	e.pathDir = filepath.Join(e.root, "bin")
	e.git(e.root, "init", "-q", "--bare", e.remote)
	e.git(e.root, "init", "-q", "-b", "main", e.repo)
	e.git(e.repo, "config", "user.name", "t")
	e.git(e.repo, "config", "user.email", "t@example.com")
	e.git(e.repo, "remote", "add", "origin", e.remote)
	e.commitFile("base.txt", "base")
	e.git(e.repo, "push", "-q", "-u", "origin", "main")

	// The hook also marks each push in the gh log, so tests can see how pushes
	// and PR edits interleave.
	hook := fmt.Sprintf("#!/bin/sh\ncat > /dev/null\necho push >> %q\necho PUSH >> %q\n", e.pushLog, e.ghLog)
	if err := os.WriteFile(filepath.Join(e.remote, "hooks", "pre-receive"), []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(e.pathDir, 0755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(e.pathDir, "git")); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(e.root, "pr-counter")
	// The fake remembers each PR's base (from create/edit --base) so that
	// `pr view` reports what GitHub would.
	fake := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]q
base_of() { prev=""; for a in "$@"; do if [ "$prev" = "--base" ]; then echo "$a"; return; fi; prev="$a"; done; }
STACK=%[2]q.stack   # the registered Stack: one PR number per line
case "$1 $2" in
  "pr create")
    case "$*" in *"--head commit-b-"*) if [ -e %[2]q.fail-b ]; then echo "GraphQL: boom" >&2; exit 1; fi ;; esac
    # creates run concurrently: take a lock to number them atomically
    while ! mkdir %[2]q.lock 2>/dev/null; do sleep 0.01; done
    n=$(( $(cat %[2]q 2>/dev/null || echo 100) + 1 )); echo $n > %[2]q
    rmdir %[2]q.lock
    echo CREATE_START >> %[1]q
    sleep $(cat %[2]q.delay 2>/dev/null || echo 0.3)
    echo CREATE_END >> %[1]q
    base_of "$@" > %[2]q-base-$n; echo "https://github.com/o/r/pull/$n" ;;
  "pr view") b=$(cat %[2]q-base-$3 2>/dev/null || echo main); echo '{"number":'"$3"',"url":"https://github.com/o/r/pull/'"$3"'","state":"OPEN","baseRefName":"'"$b"'"}' ;;
  "pr edit")
    # like GitHub: any --base is rejected on a PR in a registered Stack
    if [ -n "$(base_of "$@")" ] && [ -e "$STACK" ] && grep -qx "$3" "$STACK"; then
      echo "GraphQL: Cannot change the base branch because the pull request is part of a stack. (updatePullRequest)" >&2; exit 1
    fi
    b=$(base_of "$@"); if [ -n "$b" ]; then echo "$b" > %[2]q-base-$3; fi ;;
  *)
    case "$*" in
      *"--method GET"*stacks/*) if [ -e "$STACK" ]; then printf '{"number":7,"pull_requests":['; paste -sd, "$STACK" | sed 's/[0-9][0-9]*/{"number":&}/g' | tr -d '\n'; printf ']}\n'; else echo "HTTP 404: Not Found" >&2; exit 1; fi ;;
      *unstack*) rm -f "$STACK"; echo '{}' ;;
      *"--method POST"*stacks) body=$(cat); echo "$body" | sed 's/[^0-9,]//g' | tr ',' '\n' > "$STACK"; echo '{"number":7}' ;;
      *) echo '{"number":7}' ;;
    esac ;;
esac
`, e.ghLog, counter)
	if err := os.WriteFile(filepath.Join(e.pathDir, "gh"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *batchPushEnv) git(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *batchPushEnv) commitFile(name, msg string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.repo, name), []byte(msg+"\n"), 0644); err != nil {
		e.t.Fatal(err)
	}
	e.git(e.repo, "add", name)
	e.git(e.repo, "commit", "-q", "-m", msg)
}

// sd runs git-sd in the repo with the fake gh first on PATH.
func (e *batchPushEnv) sd(args ...string) (string, error) {
	cmd := exec.Command(gitSDPath, args...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(), "PATH="+e.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *batchPushEnv) lines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (e *batchPushEnv) ghCalls(prefix string) []string {
	var out []string
	for _, l := range e.lines(e.ghLog) {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// stackCalls counts the Stacks API calls made so far: reads, tear-downs and
// registrations.
func (e *batchPushEnv) stackCalls() (gets, unstacks, creates int) {
	for _, l := range e.lines(e.ghLog) {
		switch {
		case strings.HasPrefix(l, "api --method GET") && strings.Contains(l, "/stacks/"):
			gets++
		case strings.HasPrefix(l, "api --method POST") && strings.HasSuffix(l, "/unstack"):
			unstacks++
		case strings.HasPrefix(l, "api --method POST --input -") && strings.HasSuffix(l, "/stacks"):
			creates++
		}
	}
	return
}

// remoteBranch returns the single remote branch whose name starts with prefix
// and its tip, or "" if there is none.
func (e *batchPushEnv) remoteBranch(prefix string) (name, tip string) {
	out := e.git(e.remote, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/"+prefix+"*")
	if out == "" {
		return "", ""
	}
	name, tip, _ = strings.Cut(strings.Split(out, "\n")[0], " ")
	return name, tip
}

// TestSDSyncPushesAWholeStackInOnePush checks that every branch of a stack goes
// out in a single push, and that the PRs are still created in order, each
// based on the previous branch.
func TestSDSyncPushesAWholeStackInOnePush(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C", "Commit D"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if out, err := e.sd("assign", "tmp:0-3"); err != nil {
		t.Fatalf("assign: %v\n%s", err, out)
	}
	out, err := e.sd("sync")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	if n := len(e.lines(e.pushLog)); n != 1 {
		t.Errorf("expected the 4 branches in 1 push, got %d pushes", n)
	}
	creates := e.ghCalls("pr create")
	if len(creates) != 4 {
		t.Fatalf("expected 4 PRs created, got %d: %v", len(creates), creates)
	}
	// PRs are created concurrently, so the calls may come in any order; each
	// must still be for the right head on the previous branch.
	prev := "main"
	for _, letter := range []string{"a", "b", "c", "d"} {
		name, _ := e.remoteBranch("commit-" + letter + "-")
		if name == "" {
			t.Fatalf("remote branch for commit %s is missing", letter)
		}
		found := false
		for _, c := range creates {
			if strings.Contains(c, "--head "+name) {
				found = true
				if !strings.Contains(c, "--base "+prev+" ") {
					t.Errorf("PR for %s should be on base %s, got: %s", name, prev, c)
				}
			}
		}
		if !found {
			t.Errorf("no PR was created for %s: %v", name, creates)
		}
		prev = name
	}
	// The creates really overlapped (the fake takes 0.3 s per create).
	inFlight, peak := 0, 0
	for _, l := range e.lines(e.ghLog) {
		switch l {
		case "CREATE_START":
			inFlight++
			peak = max(peak, inFlight)
		case "CREATE_END":
			inFlight--
		}
	}
	if peak < 2 {
		t.Errorf("expected PR creation to run concurrently, peak in flight was %d", peak)
	}
	// Every PR got its own number, and status lists them all in stack order.
	status, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	for _, n := range []string{"101", "102", "103", "104"} {
		if strings.Count(status, "/pull/"+n) != 1 {
			t.Errorf("PR %s should be recorded exactly once:\n%s", n, status)
		}
	}
	// Branches are a chain: each one's parent is the previous branch's tip.
	prevTip := e.git(e.repo, "rev-parse", "origin/main")
	for _, letter := range []string{"a", "b", "c", "d"} {
		name, tip := e.remoteBranch("commit-" + letter + "-")
		if parent := e.git(e.remote, "rev-parse", name+"^"); parent != prevTip {
			t.Errorf("%s's parent is %s, want the previous tip %s", name, parent, prevTip)
		}
		prevTip = tip
	}

	// Nothing changed: no push receives anything, no PR is created or edited.
	before := len(e.ghCalls("pr create")) + len(e.ghCalls("pr edit"))
	out, err = e.sd("sync")
	if err != nil {
		t.Fatalf("resync: %v\n%s", err, out)
	}
	if n := len(e.lines(e.pushLog)); n != 1 {
		t.Errorf("an unchanged re-sync must not update the remote, pushes = %d", n)
	}
	if after := len(e.ghCalls("pr create")) + len(e.ghCalls("pr edit")); after != before {
		t.Errorf("an unchanged re-sync must not create or edit PRs (%d -> %d calls)", before, after)
	}
	// ...and the registered Stack is read, not torn down and rebuilt.
	if gets, unstacks, creates := e.stackCalls(); unstacks != 0 || creates != 1 || gets != 1 {
		t.Errorf("an unchanged re-sync should only read the Stack (1 GET, 0 unstack, 1 create in total), got %d GET, %d unstack, %d create", gets, unstacks, creates)
	}
}

// TestSDSyncReplansLaterGroupsAfterARejectedPush rejects one branch at the
// remote and checks the failure is contained: its own group is reported and
// skipped, groups in the same Stack Set after it are re-planned from the last
// good tip, and a later sync catches up.
func TestSDSyncReplansLaterGroupsAfterARejectedPush(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	hook := filepath.Join(e.remote, "hooks", "update")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncase \"$1\" in refs/heads/commit-b-*) echo 'B is not allowed' >&2; exit 1;; esac\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := e.sd("assign", "tmp:0-2"); err != nil {
		t.Fatalf("assign: %v\n%s", err, out)
	}

	out, err := e.sd("sync")
	if err == nil {
		t.Fatalf("a sync with a failed group must exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "Error: could not push branch commit-b-") {
		t.Errorf("the rejected push should be reported:\n%s", out)
	}
	if !strings.Contains(out, "Sync completed with 1 error.") {
		t.Errorf("the closing line should count the error:\n%s", out)
	}
	if name, _ := e.remoteBranch("commit-b-"); name != "" {
		t.Errorf("B must not exist on the remote, found %s", name)
	}
	nameA, tipA := e.remoteBranch("commit-a-")
	nameC, _ := e.remoteBranch("commit-c-")
	if nameA == "" || nameC == "" {
		t.Fatalf("A and C should be pushed: a=%q c=%q", nameA, nameC)
	}
	// C was re-planned onto A (the last good tip), not left built on rejected B.
	if parent := e.git(e.remote, "rev-parse", nameC+"^"); parent != tipA {
		t.Errorf("C's parent is %s, want A's tip %s", parent, tipA)
	}
	creates := e.ghCalls("pr create")
	if len(creates) != 2 {
		t.Fatalf("expected PRs for A and C only, got %v", creates)
	}
	if !strings.Contains(creates[1], "--head "+nameC) || !strings.Contains(creates[1], "--base "+nameA+" ") {
		t.Errorf("C's PR should be based on A's branch: %s", creates[1])
	}

	// Allow B and sync again: B is created, and C is rebuilt on B and retargeted.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	out, err = e.sd("sync")
	if err != nil {
		t.Fatalf("second sync: %v\n%s", err, out)
	}
	nameB, tipB := e.remoteBranch("commit-b-")
	if nameB == "" {
		t.Fatalf("B should be pushed by the second sync:\n%s", out)
	}
	if len(e.ghCalls("pr create")) != 3 {
		t.Errorf("B's PR should have been created, creates = %v", e.ghCalls("pr create"))
	}
	_, tipC := e.remoteBranch("commit-c-")
	if parent := e.git(e.remote, "rev-parse", nameC+"^"); parent != tipB {
		t.Errorf("C should now sit on B (tip %s), parent is %s (tipC %s)", tipB, parent, tipC)
	}
	edits := strings.Join(e.ghCalls("pr edit"), "\n")
	if !strings.Contains(edits, "--base "+nameB) {
		t.Errorf("C's PR should be retargeted onto B, edits:\n%s", edits)
	}
}

// TestSDSyncRetargetsAPRBeforeTheNextBranchMovesAfterAReorder guards the one
// case where batching pushes is unsafe: after a reorder, pushing every branch
// first can leave a PR's new head an ancestor of its old base, and GitHub then
// marks it merged. When a base changes, each group must be pushed and its PR
// retargeted before the next branch is pushed.
func TestSDSyncRetargetsAPRBeforeTheNextBranchMovesAfterAReorder(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if out, err := e.sd("assign", "tmp:0-1"); err != nil {
		t.Fatalf("assign: %v\n%s", err, out)
	}
	if out, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if n := len(e.lines(e.pushLog)); n != 1 {
		t.Fatalf("the first sync should be one push, got %d", n)
	}
	if out, err := e.sd("move", "0:1"); err != nil {
		t.Fatalf("move: %v\n%s", err, out)
	}
	mark := len(e.lines(e.ghLog))
	if out, err := e.sd("sync"); err != nil {
		t.Fatalf("sync after reorder: %v\n%s", err, out)
	}

	// Between the two pushes of the second sync there must be a PR edit.
	var seq []string
	for _, l := range e.lines(e.ghLog)[mark:] {
		switch {
		case l == "PUSH":
			seq = append(seq, "push")
		case strings.HasPrefix(l, "pr edit"):
			seq = append(seq, "edit")
		}
	}
	// The Stack comes down before the first edit that changes a base (the fake
	// rejects any --base on a PR that is still stacked).
	unstackAt, editAt := -1, -1
	for i, l := range e.lines(e.ghLog)[mark:] {
		if strings.HasSuffix(l, "/unstack") && unstackAt < 0 {
			unstackAt = i
		}
		if strings.HasPrefix(l, "pr edit") && strings.Contains(l, "--base") && editAt < 0 {
			editAt = i
		}
	}
	if unstackAt < 0 || editAt < 0 || unstackAt > editAt {
		t.Errorf("the Stack must be unstacked before the first base edit (unstack at %d, edit at %d)", unstackAt, editAt)
	}
	got := strings.Join(seq, " ")
	if got != "push edit push edit" {
		t.Errorf("after a reorder, expected each group pushed then retargeted (push edit push edit), got: %s", got)
	}
}

// TestSDSyncAFailedPRCreateLeavesOnlyThatGroupWithoutAPR fails creating one PR
// of three and checks the others are unaffected: the failure is reported, the
// failed group has no PR, the later group keeps its PR (on the failed group's
// already-pushed branch), and the next sync creates the missing PR without
// touching the rest.
func TestSDSyncAFailedPRCreateLeavesOnlyThatGroupWithoutAPR(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if out, err := e.sd("assign", "tmp:0-2"); err != nil {
		t.Fatalf("assign: %v\n%s", err, out)
	}
	failFile := filepath.Join(e.root, "pr-counter.fail-b")
	if err := os.WriteFile(failFile, nil, 0644); err != nil {
		t.Fatal(err)
	}

	out, err := e.sd("sync")
	if err == nil {
		t.Fatalf("a sync with a failed group must exit non-zero:\n%s", out)
	}
	nameA, _ := e.remoteBranch("commit-a-")
	nameB, _ := e.remoteBranch("commit-b-")
	nameC, _ := e.remoteBranch("commit-c-")
	if nameA == "" || nameB == "" || nameC == "" {
		t.Fatalf("all three branches should be pushed: %q %q %q", nameA, nameB, nameC)
	}
	// The failure names the branch and says how to recover.
	if !strings.Contains(out, "Error: could not create a PR for branch "+nameB+": ") {
		t.Errorf("the failed create should name its branch:\n%s", out)
	}
	if !strings.Contains(out, "No PR was created for: "+nameB+". Fix the error above and re-run 'git sd sync' to create it") {
		t.Errorf("the recovery hint is missing:\n%s", out)
	}
	if strings.Contains(out, nameA+". Fix") || strings.Contains(out, nameC+". Fix") {
		t.Errorf("the hint must only name the branch that failed:\n%s", out)
	}
	// Two PRs exist (A and C); C sits on B's pushed branch, not on A.
	var cCreate string
	for _, c := range e.ghCalls("pr create") {
		if strings.Contains(c, "--head "+nameC) {
			cCreate = c
		}
	}
	if cCreate == "" || !strings.Contains(cCreate, "--base "+nameB+" ") {
		t.Errorf("C's PR should be based on B's branch: %q", cCreate)
	}
	if strings.Contains(out, "could not register stack") {
		t.Errorf("an incomplete chain must not be registered (GitHub refuses it), so no second error:\n%s", out)
	}
	if _, _, creates := e.stackCalls(); creates != 0 {
		t.Errorf("no Stack should be registered while a PR is missing, got %d registrations", creates)
	}
	status, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if got := strings.Count(status, "/pull/"); got != 2 {
		t.Errorf("exactly 2 PRs should be recorded after the failed create, got %d:\n%s", got, status)
	}

	// Let creates succeed again: only B's PR is created; nothing else is edited.
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	editsBefore := len(e.ghCalls("pr edit"))
	if out, err := e.sd("sync"); err != nil {
		t.Fatalf("second sync: %v\n%s", err, out)
	}
	status, _ = e.sd("status")
	if got := strings.Count(status, "/pull/"); got != 3 {
		t.Errorf("all 3 PRs should exist after the second sync, got %d:\n%s", got, status)
	}
	if got := len(e.ghCalls("pr edit")) - editsBefore; got != 0 {
		t.Errorf("the second sync should only create B's PR, but edited %d PR(s)", got)
	}
	if _, _, creates := e.stackCalls(); creates != 1 {
		t.Errorf("the second sync should register the complete Stack once, got %d registrations", creates)
	}
}

// TestSDSyncKeepsTheStackUnlessItMustChange checks when sync touches a
// registered Stack: a title edit and a push-only amend leave it alone, while a
// new PR on top (a different PR list) re-registers it.
func TestSDSyncKeepsTheStackUnlessItMustChange(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if out, err := e.sd("assign", "tmp:0-2"); err != nil {
		t.Fatalf("assign: %v\n%s", err, out)
	}
	if out, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if _, unstacks, creates := e.stackCalls(); unstacks != 0 || creates != 1 {
		t.Fatalf("the first sync registers the Stack once: %d unstack, %d create", unstacks, creates)
	}

	// A new title for the top commit: a title edit, no base change.
	msg := e.git(e.repo, "log", "-1", "--format=%B") // keeps the git-sd-id trailer
	e.git(e.repo, "commit", "-q", "--amend", "-m", strings.Replace(msg, "Commit C", "Commit C renamed", 1))
	editsBefore := len(e.ghCalls("pr edit"))
	if out, err := e.sd("sync"); err != nil || strings.Contains(out, "Error") {
		t.Fatalf("sync after a title change: %v\n%s", err, out)
	}
	edits := e.ghCalls("pr edit")[editsBefore:]
	if len(edits) != 1 || strings.Contains(edits[0], "--base") {
		t.Errorf("a title change should be one edit without --base (GitHub rejects --base on a stacked PR), got %v", edits)
	}
	if _, unstacks, creates := e.stackCalls(); unstacks != 0 || creates != 1 {
		t.Errorf("a title change must leave the Stack alone: %d unstack, %d create", unstacks, creates)
	}

	// Content changes (an amend that only changes a file) need no Stack change either.
	if err := os.WriteFile(filepath.Join(e.repo, "Commit_C.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.git(e.repo, "commit", "-q", "-a", "--amend", "--no-edit")
	if out, err := e.sd("sync"); err != nil || strings.Contains(out, "Error") {
		t.Fatalf("sync after a content amend: %v\n%s", err, out)
	}
	if _, unstacks, creates := e.stackCalls(); unstacks != 0 || creates != 1 {
		t.Errorf("a content amend must leave the Stack alone: %d unstack, %d create", unstacks, creates)
	}

	// A new commit on top changes the PR list: tear down and register again.
	e.commitFile("Commit_D.txt", "Commit D")
	if out, err := e.sd("assign", "tmp:3"); err != nil {
		t.Fatalf("assign D: %v\n%s", err, out)
	}
	if out, err := e.sd("sync"); err != nil || strings.Contains(out, "Error") {
		t.Fatalf("sync after adding D: %v\n%s", err, out)
	}
	if _, unstacks, creates := e.stackCalls(); unstacks != 1 || creates != 2 {
		t.Errorf("a longer chain must be unstacked and registered again: %d unstack, %d create", unstacks, creates)
	}
}
