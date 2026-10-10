package github

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushFixture is a local repo with a bare remote and a few commits to push.
type pushFixture struct {
	t      *testing.T
	remote string
	repo   string
}

func (f *pushFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	dir := t.TempDir()
	f := &pushFixture{t: t, remote: filepath.Join(dir, "origin.git"), repo: filepath.Join(dir, "repo")}
	f.git(dir, "init", "-q", "--bare", f.remote)
	f.git(dir, "init", "-q", "-b", "main", f.repo)
	f.git(f.repo, "config", "user.name", "t")
	f.git(f.repo, "config", "user.email", "t@example.com")
	f.git(f.repo, "remote", "add", "origin", f.remote)
	return f
}

// commit makes a commit touching its own file and returns its hash.
func (f *pushFixture) commit(name string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, name), []byte(name+"\n"), 0644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.repo, "add", name)
	f.git(f.repo, "commit", "-q", "-m", name)
	return f.git(f.repo, "rev-parse", "HEAD")
}

func (f *pushFixture) remoteRef(branch string) string {
	f.t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	cmd.Dir = f.remote
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func inRepo(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestPushBranchesReportsEachBranchAndPushesOnlyChanges(t *testing.T) {
	fakeGH(t, "ok") // ensureReady needs a gh that can authenticate
	f := newPushFixture(t)
	inRepo(t, f.repo)
	a, b, c := f.commit("a"), f.commit("b"), f.commit("c")
	pushes := []BranchPush{{a, "br-a"}, {b, "br-b"}, {c, "br-c"}}

	res, err := PushBranches("origin", pushes)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || !r.Pushed {
			t.Errorf("first push, branch %d: %+v, want pushed", i, r)
		}
	}
	for _, p := range pushes {
		if got := f.remoteRef(p.Branch); got != p.Local {
			t.Errorf("remote %s = %s, want %s", p.Branch, got, p.Local)
		}
	}

	res, err = PushBranches("origin", pushes)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || r.Pushed {
			t.Errorf("repeat push, branch %d: %+v, want up to date (not pushed)", i, r)
		}
	}

	d := f.commit("d")
	pushes[1] = BranchPush{d, "br-b"} // only br-b changes (a forced, non-fast-forward-safe update)
	res, err = PushBranches("origin", pushes)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Pushed || !res[1].Pushed || res[2].Pushed || res[0].Err != nil || res[1].Err != nil || res[2].Err != nil {
		t.Errorf("only the changed branch should report pushed: %+v", res)
	}
	if got := f.remoteRef("br-b"); got != d {
		t.Errorf("remote br-b = %s, want %s", got, d)
	}
}

func TestPushBranchesRejectedRefFailsOnlyItself(t *testing.T) {
	fakeGH(t, "ok")
	f := newPushFixture(t)
	inRepo(t, f.repo)
	hook := filepath.Join(f.remote, "hooks", "update")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncase \"$1\" in refs/heads/br-bad) echo 'not allowed' >&2; exit 1;; esac\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a, b, c := f.commit("a"), f.commit("b"), f.commit("c")
	res, err := PushBranches("origin", []BranchPush{{a, "br-a"}, {b, "br-bad"}, {c, "br-c"}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil || !res[0].Pushed || res[2].Err != nil || !res[2].Pushed {
		t.Errorf("the other branches must still be pushed: %+v", res)
	}
	if res[1].Err == nil {
		t.Errorf("the rejected branch must report an error: %+v", res[1])
	}
	if f.remoteRef("br-bad") != "" || f.remoteRef("br-a") != a || f.remoteRef("br-c") != c {
		t.Errorf("remote state wrong: bad=%q a=%q c=%q", f.remoteRef("br-bad"), f.remoteRef("br-a"), f.remoteRef("br-c"))
	}
}

func TestPushBranchesWholePushFailureFailsEveryBranch(t *testing.T) {
	fakeGH(t, "ok")
	f := newPushFixture(t)
	inRepo(t, f.repo)
	a, b := f.commit("a"), f.commit("b")
	res, err := PushBranches("no-such-remote", []BranchPush{{a, "br-a"}, {b, "br-b"}})
	if err != nil {
		t.Fatalf("a failed push is a per-branch error, not an environment error: %v", err)
	}
	for i, r := range res {
		if r.Err == nil || r.Pushed {
			t.Errorf("branch %d should have failed: %+v", i, r)
		}
	}
}

func TestPushBranchesWithNothingToPushDoesNotCallGH(t *testing.T) {
	logPath := fakeGH(t, "ok")
	res, err := PushBranches("origin", nil)
	if err != nil || len(res) != 0 {
		t.Fatalf("got %v, %v", res, err)
	}
	if n := callCount(t, logPath); n != 0 {
		t.Errorf("expected no gh calls, got %d", n)
	}
}

func TestPushBranchesNeedsAUsableGH(t *testing.T) {
	fakeGH(t, "auth4")
	f := newPushFixture(t)
	inRepo(t, f.repo)
	a := f.commit("a")
	if _, err := PushBranches("origin", []BranchPush{{a, "br-a"}}); !IsEnvError(err) {
		t.Fatalf("expected an environment error before any push, got %v", err)
	}
	if f.remoteRef("br-a") != "" {
		t.Error("nothing should have been pushed when gh is unusable")
	}
}

func TestParsePushPorcelain(t *testing.T) {
	out := "To /tmp/x.git\n*\tabc:refs/heads/new\t[new branch]\n=\tdef:refs/heads/same\t[up to date]\n+\t123:refs/heads/forced\told...new (forced update)\n!\t456:refs/heads/bad\t[remote rejected] (hook declined)\nDone\n"
	got := parsePushPorcelain(out)
	want := map[string]byte{"refs/heads/new": '*', "refs/heads/same": '=', "refs/heads/forced": '+', "refs/heads/bad": '!'}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for ref, flag := range want {
		if got[ref].flag != flag {
			t.Errorf("%s: flag %q, want %q", ref, got[ref].flag, flag)
		}
	}
	if !strings.Contains(got["refs/heads/bad"].summary, "hook declined") {
		t.Errorf("summary lost: %q", got["refs/heads/bad"].summary)
	}
}
