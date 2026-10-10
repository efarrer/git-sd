package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSDWatchReportsChangesAndSurvivesAnOutage runs a real `git sd watch` against
// the fake gh while the test changes the PR's state: CI starts, CI passes and
// the PR is approved, GitHub becomes unreachable and comes back. It checks the
// event lines, the notification command's environment, that the metadata is
// never written, and a clean exit on SIGINT.
func TestSDWatchReportsChangesAndSurvivesAnOutage(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	e.commitFile("Commit_A.txt", "Commit A")
	e.commitFile("Commit_B.txt", "Commit B")
	if o, err := e.sd("assign", "tmp:0-1"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	if o, err := e.sd("sync"); err != nil { // two PRs; the fake reports them open and unreviewed (Review)
		t.Fatalf("sync: %v\n%s", err, o)
	}
	// A sub-commit on the first PR, so the table has a row that has no PR of its own.
	e.commitFile("Fix_A.txt", "Fix for A")
	if o, err := e.sd("track"); err != nil {
		t.Fatalf("track: %v\n%s", err, o)
	}
	if o, err := e.sd("fixup", "0"); err != nil {
		t.Fatalf("fixup: %v\n%s", err, o)
	}
	st, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, st)
	}
	prs := extractStackPRNumbers(st, "tmp") // in stack order; the second one is the one we will change
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %v", prs)
	}
	second := prs[1]
	statusTable := st // what `git sd status` prints right now; watch must open with exactly this
	notified := filepath.Join(e.root, "notified.log")
	e.git(e.repo, "config", "sd.watch.min-interval", "100ms")
	e.git(e.repo, "config", "sd.watch.max-interval", "300ms")
	e.git(e.repo, "config", "sd.watch.notify-command", `echo "$GIT_SD_EVENT|$GIT_SD_PR_NUMBER|$GIT_SD_FROM -> $GIT_SD_TO|$GIT_SD_TITLE|$GIT_SD_MESSAGE" >> `+notified)

	metaPath := filepath.Join(e.repo, ".git", "sd", "metadata.json")
	before, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(e.root, "watch.out")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gitSDPath, "watch")
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(), "PATH="+e.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stdout, cmd.Stderr = outFile, outFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	read := func(path string) string { data, _ := os.ReadFile(path); return string(data) }
	waitFor := func(path, needle, what string) {
		t.Helper()
		for range 200 {
			if strings.Contains(read(path), needle) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (%q) in %s:\n%s", what, needle, filepath.Base(path), read(path))
	}
	setState := func(rollup, decision string) {
		t.Helper()
		body := `{"number":` + second + `,"url":"https://github.com/o/r/pull/` + second + `","state":"OPEN","isDraft":false,"reviewDecision":"` + decision + `","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","statusCheckRollup":` + rollup + `}`
		if err := os.WriteFile(filepath.Join(e.root, "pr-counter-pr-"+second+".json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Baseline: the table, no events.
	waitFor(outPath, "/pull/"+second, "the baseline table")
	if !strings.Contains(statusTable, "└─ Fix for A") || !strings.Contains(statusTable, "Unsynced") {
		t.Fatalf("test premise: status shows the sub-commit and its Unsynced marker:\n%s", statusTable)
	}
	if got := read(outPath); !strings.HasPrefix(got, statusTable) {
		t.Fatalf("watch must open with exactly the table status prints, sub-commit row included.\n--- status ---\n%s\n--- watch ---\n%s", statusTable, got)
	}
	if strings.Contains(read(outPath), "→") {
		t.Fatalf("the baseline must not report changes:\n%s", read(outPath))
	}

	// CI starts.
	setState(`[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]`, "")
	waitFor(outPath, "tmp 2/2  PR #"+second+"  Review → CI running", "the CI-started event, naming the set and the PR's place in it")
	waitFor(notified, "ci-running|"+second+"|Review -> CI running|tmp 2/2: CI started|CI started on PR 2 of 2 in tmp (#"+second+"): Commit B", "its notification, with a title and message that identify the PR")

	// CI passes and the PR is approved.
	setState(`[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]`, "APPROVED")
	waitFor(outPath, "tmp 2/2  PR #"+second+"  CI running → Approved", "the CI-passed event")
	waitFor(notified, "ci-passed|"+second+"|CI running -> Approved|tmp 2/2: CI passed|CI passed on PR 2 of 2 in tmp (#"+second+"), now Approved: Commit B", "its notification")

	// GitHub becomes unreachable, then recovers.
	if err := os.WriteFile(filepath.Join(e.root, "pr-counter.pr-fail"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	waitFor(outPath, "Cannot reach GitHub", "the outage message")
	waitFor(notified, "offline", "the offline notification")
	if err := os.Remove(filepath.Join(e.root, "pr-counter.pr-fail")); err != nil {
		t.Fatal(err)
	}
	waitFor(outPath, "Back online.", "the recovery message")
	if got := strings.Count(read(outPath), "Cannot reach GitHub"); got != 1 {
		t.Errorf("the outage is announced once, got %d:\n%s", got, read(outPath))
	}
	if strings.Contains(read(outPath), "Approved → ?") || strings.Count(read(outPath), "→") != 2 {
		t.Errorf("neither the outage nor the recovery is a PR change:\n%s", read(outPath))
	}

	// Ctrl-C ends it cleanly.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("a watch stopped with Ctrl-C exits 0, got %v\n%s", err, read(outPath))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not stop on SIGINT:\n%s", read(outPath))
	}
	if !strings.Contains(read(outPath), "Stopped watching.") {
		t.Errorf("a stop line is printed:\n%s", read(outPath))
	}
	after, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("watch is read-only: metadata.json was rewritten")
	}
}

// TestSDWatchRejectsBadSettingsAndArguments covers the errors that stop it at once.
func TestSDWatchRejectsBadSettingsAndArguments(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	e.commitFile("Commit_A.txt", "Commit A")
	e.git(e.repo, "config", "sd.status.columns", "Colour")
	if out, err := e.sd("watch"); err == nil || !strings.Contains(out, "unknown status column 'Colour'") {
		t.Errorf("watch uses status's columns, so a bad column is the same error: %v\n%s", err, out)
	}
	e.git(e.repo, "config", "--unset", "sd.status.columns")
	if out, err := e.sd("watch", "extra"); err == nil || !strings.Contains(out, "watch takes no arguments") {
		t.Errorf("arguments are refused: %v\n%s", err, out)
	}
	e.git(e.repo, "config", "sd.watch.min-interval", "soon")
	if out, err := e.sd("watch"); err == nil || !strings.Contains(out, "Error: sd.watch.min-interval must be") {
		t.Errorf("a bad interval is refused: %v\n%s", err, out)
	}
	e.git(e.repo, "config", "sd.watch.min-interval", "30s")
	e.git(e.repo, "config", "sd.watch.max-interval", "10s")
	if out, err := e.sd("watch"); err == nil || !strings.Contains(out, "must not be shorter") {
		t.Errorf("a maximum below the minimum is refused: %v\n%s", err, out)
	}
}
