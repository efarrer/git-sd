package main

import (
	"encoding/json"
	"git-sd/internal/ui"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// inTempRepo runs the test inside a fresh git repo, with no lock held.
func inTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	heldOperation = ""
	t.Cleanup(func() { heldOperation = ""; os.Chdir(old) })
	return dir
}

func writeOperation(t *testing.T, op operation) string {
	t.Helper()
	path, err := operationPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(op)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBeginAndEndOperation(t *testing.T) {
	inTempRepo(t)
	if op := activeOperation(); op != nil {
		t.Fatalf("no operation should be active in a fresh repo, got %+v", op)
	}
	beginOperation("sync", "feat-a")
	op := activeOperation()
	if op == nil || op.Command != "sync" || op.Set != "feat-a" || op.PID != os.Getpid() {
		t.Fatalf("expected our sync of feat-a, got %+v", op)
	}
	endOperation()
	if op := activeOperation(); op != nil {
		t.Errorf("the lock should be gone after endOperation, got %+v", op)
	}
	endOperation() // idempotent
}

func TestExitReleasesTheLockButNotSomeoneElses(t *testing.T) {
	inTempRepo(t)
	beginOperation("merge", "")
	path, _ := operationPath()
	// Another process took over the file: ours must leave it alone.
	writeOperation(t, operation{Command: "sync", PID: os.Getpid() + 1, Started: time.Now()})
	endOperation()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("endOperation must not remove a lock written by another process: %v", err)
	}
}

func TestStaleLocksAreIgnoredAndRemoved(t *testing.T) {
	inTempRepo(t)
	// A process that has already exited: its PID is dead.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := cmd.Process.Pid

	cases := []struct {
		name string
		op   operation
	}{
		{"its process is gone (crashed or killed)", operation{Command: "sync", PID: dead, Started: time.Now()}},
		{"older than the age cap even though the PID answers", operation{Command: "sync", PID: os.Getpid(), Started: time.Now().Add(-3 * time.Hour)}},
		{"no PID", operation{Command: "sync", Started: time.Now()}},
	}
	for _, c := range cases {
		path := writeOperation(t, c.op)
		if got := activeOperation(); got != nil {
			t.Errorf("%s: should be ignored, got %+v", c.name, got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: the stale lock should have been removed", c.name)
		}
	}

	path, _ := operationPath()
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := activeOperation(); got != nil {
		t.Errorf("a corrupt lock should be ignored, got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a corrupt lock should be removed")
	}
}

func TestOperationAffects(t *testing.T) {
	all := &operation{Command: "sync"}
	one := &operation{Command: "sync", Set: "feat-a"}
	cases := []struct {
		name string
		op   *operation
		set  string
		want bool
	}{
		{"no operation", nil, "feat-a", false},
		{"an operation on every set", all, "feat-a", true},
		{"an operation on one set, that set", one, "feat-a", true},
		{"an operation on one set, another set", one, "feat-b", false},
		{"commits with no Stack Set are never affected", all, "", false},
	}
	for _, c := range cases {
		if got := c.op.affects(c.set); got != c.want {
			t.Errorf("%s: affects(%q) = %v, want %v", c.name, c.set, got, c.want)
		}
	}
}

func TestWithOperation(t *testing.T) {
	for _, id := range []ui.MarkerID{ui.Assigned, ui.Review, ui.Approved, ui.Draft, ui.Conflict, ui.Changes, ui.CIFailed, ui.CIRunning} {
		if got := withOperation(id, true); got != ui.Syncing {
			t.Errorf("%s while syncing should be Syncing, got %s", id, got)
		}
		if got := withOperation(id, false); got != id {
			t.Errorf("%s when nothing is running must be unchanged, got %s", id, got)
		}
	}
	for _, id := range []ui.MarkerID{ui.Merged, ui.Closed, ui.Unavailable} {
		if got := withOperation(id, true); got != id {
			t.Errorf("%s ranks above Syncing and must be kept, got %s", id, got)
		}
	}
}
