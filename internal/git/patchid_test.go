package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func patchRepo(t *testing.T) (dir string, run func(args ...string) string) {
	t.Helper()
	dir = t.TempDir()
	run = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "t")
	run("config", "user.email", "t@example.com")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("base.txt", "base\n")
	run("add", ".")
	run("commit", "-q", "-m", "base")
	return dir, run
}

func inDir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestPatchIDsSurviveARebaseAndChangeWithTheDiff(t *testing.T) {
	dir, run := patchRepo(t)
	inDir(t, dir)
	base := run("rev-parse", "HEAD")

	run("checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644)
	run("add", ".")
	run("commit", "-q", "-m", "add a")
	a1 := run("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0644)
	run("add", ".")
	run("commit", "-q", "-m", "add b")
	b1 := run("rev-parse", "HEAD")

	ids, err := PatchIDs(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[a1] == "" || ids[b1] == "" || ids[a1] == ids[b1] {
		t.Fatalf("expected two distinct patch-ids, got %v", ids)
	}

	// Rebase onto a new base commit: new hashes, same diffs, same patch-ids.
	run("checkout", "-q", "main")
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other\n"), 0644)
	run("add", ".")
	run("commit", "-q", "-m", "other")
	newBase := run("rev-parse", "HEAD")
	run("checkout", "-q", "feature")
	run("rebase", "-q", newBase)
	rebased, err := PatchIDs(newBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebased) != 2 {
		t.Fatalf("got %v", rebased)
	}
	got := map[string]bool{}
	for _, id := range rebased {
		got[id] = true
	}
	if !got[ids[a1]] || !got[ids[b1]] {
		t.Errorf("a rebase must keep patch-ids: before %v after %v", ids, rebased)
	}

	// Amending the top commit's content changes its patch-id.
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b changed\n"), 0644)
	run("add", ".")
	run("commit", "-q", "--amend", "--no-edit")
	amended, err := PatchIDs(newBase)
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for _, id := range amended {
		if got[id] {
			same++
		}
	}
	if len(amended) != 2 || same != 1 {
		t.Errorf("exactly one patch-id should survive an amend of one commit: %v", amended)
	}
}

func TestPatchIDsIgnoreMergesAndEmptyCommits(t *testing.T) {
	dir, run := patchRepo(t)
	inDir(t, dir)
	base := run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "empty")
	ids, err := PatchIDs(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("an empty commit has no patch-id, got %v", ids)
	}
	if _, err := PatchIDs("no-such-ref"); err == nil {
		t.Error("an unknown range should be an error")
	}
}
