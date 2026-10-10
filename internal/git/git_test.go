package git

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestParseBaseBranch(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		wantErr    bool
		wantRemote string
		wantBranch string
	}{
		{"origin main", "origin/main", false, "origin", "main"},
		{"non-origin remote", "upstream/main", false, "upstream", "main"},
		{"nested branch name", "origin/feature/x", false, "origin", "feature/x"},
		{"no slash", "testing_main", true, "", ""},
		{"empty", "", true, "", ""},
		{"empty branch", "origin/", true, "", ""},
		{"empty remote", "/main", true, "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			remote, branch, err := ParseBaseBranch(c.value)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseBaseBranch(%q) = nil, want error", c.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBaseBranch(%q) = %v, want nil", c.value, err)
			}
			if remote != c.wantRemote || branch != c.wantBranch {
				t.Fatalf("ParseBaseBranch(%q) = (%q, %q), want (%q, %q)", c.value, remote, branch, c.wantRemote, c.wantBranch)
			}
		})
	}
}

// runGitT runs git in the current working directory (set via t.Chdir by the
// caller), failing the test on error, and returns trimmed stdout.
func runGitT(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// simChainFixture builds a scratch repo (in the current directory, which the
// caller must have t.Chdir'd into) with:
//   - base: a.txt with three lines.
//   - clean: a sibling commit off base that only adds a new file — applying
//     its diff onto base's own tree must reproduce clean's tree exactly.
//   - conflictA / conflictB: two commits off base that each edit base's
//     middle line differently — replaying conflictB's diff onto conflictA's
//     tree must conflict.
func simChainFixture(t *testing.T) (base, clean, conflictA, conflictB Commit) {
	t.Helper()
	runGitT(t, "init", "-q")
	runGitT(t, "config", "user.name", "git-sd-test")
	runGitT(t, "config", "user.email", "git-sd-test@example.com")

	writeAndCommit := func(file, content, msg string) Commit {
		if err := os.WriteFile(file, []byte(content), 0644); err != nil {
			t.Fatalf("writing %s: %v", file, err)
		}
		runGitT(t, "add", file)
		runGitT(t, "commit", "-q", "-m", msg)
		hash := runGitT(t, "rev-parse", "HEAD")
		msgFull := runGitT(t, "log", "-1", "--format=%B", hash)
		return Commit{Hash: hash, Message: msgFull}
	}

	base = writeAndCommit("a.txt", "line1\nline2\nline3\n", "base")
	baseRef := base.Hash

	clean = writeAndCommit("other.txt", "hello\n", "clean")

	runGitT(t, "checkout", "-q", baseRef)
	conflictA = writeAndCommit("a.txt", "line1\nCHANGED-BY-A\nline3\n", "conflictA")

	runGitT(t, "checkout", "-q", baseRef)
	conflictB = writeAndCommit("a.txt", "line1\nCHANGED-BY-B\nline3\n", "conflictB")

	runGitT(t, "checkout", "-q", baseRef)
	return base, clean, conflictA, conflictB
}

func treeOf(t *testing.T, commit string) string {
	t.Helper()
	return runGitT(t, "rev-parse", commit+"^{tree}")
}

// skipIfStrategyUnsupported skips the subtest when name is "mergeTree" but
// the installed git predates 2.38 — simulateOneCommitMergeTree is
// necessarily unusable there (that's the whole reason simulateOneCommitReadTree
// exists), so calling it directly in a test on such a machine isn't a
// regression to catch, just an expected environment limitation.
func skipIfStrategyUnsupported(t *testing.T, name string) {
	t.Helper()
	if name != "mergeTree" {
		return
	}
	major, minor, patch, err := gitVersion()
	if err == nil && atLeast(major, minor, patch, 2, 38, 0) {
		return
	}
	t.Skipf("installed git does not support 'merge-tree --write-tree' (need >= 2.38): %v", err)
}

func TestSimulateOneCommitStrategiesAgreeOnCleanApply(t *testing.T) {
	t.Chdir(t.TempDir())
	base, clean, _, _ := simChainFixture(t)

	wantTree := treeOf(t, clean.Hash)

	for name, fn := range map[string]func(string, Commit) (string, bool, error){
		"mergeTree": simulateOneCommitMergeTree,
		"readTree":  simulateOneCommitReadTree,
	} {
		t.Run(name, func(t *testing.T) {
			skipIfStrategyUnsupported(t, name)
			resultTree, conflict, err := fn(treeOf(t, base.Hash), clean)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if conflict {
				t.Fatalf("expected a clean apply, got a conflict")
			}
			if resultTree != wantTree {
				t.Fatalf("resultTree = %s, want %s", resultTree, wantTree)
			}
		})
	}
}

func TestSimulateOneCommitStrategiesAgreeOnConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, conflictA, conflictB := simChainFixture(t)

	for name, fn := range map[string]func(string, Commit) (string, bool, error){
		"mergeTree": simulateOneCommitMergeTree,
		"readTree":  simulateOneCommitReadTree,
	} {
		t.Run(name, func(t *testing.T) {
			skipIfStrategyUnsupported(t, name)
			_, conflict, err := fn(treeOf(t, conflictA.Hash), conflictB)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !conflict {
				t.Fatalf("expected a conflict, got a clean apply")
			}
		})
	}
}

func TestSimulateOneCommitStrategiesReportCommandErrorForBadHash(t *testing.T) {
	t.Chdir(t.TempDir())
	base, _, _, _ := simChainFixture(t)
	bogus := Commit{Hash: strings.Repeat("f", 40)}

	for name, fn := range map[string]func(string, Commit) (string, bool, error){
		"mergeTree": simulateOneCommitMergeTree,
		"readTree":  simulateOneCommitReadTree,
	} {
		t.Run(name, func(t *testing.T) {
			skipIfStrategyUnsupported(t, name)
			_, conflict, err := fn(treeOf(t, base.Hash), bogus)
			if err == nil {
				t.Fatalf("expected an error for a nonexistent commit hash, got nil (conflict=%v)", conflict)
			}
		})
	}
}

func TestSimulateChainBuildDetectsFirstConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	base, _, conflictA, conflictB := simChainFixture(t)

	failedAt, err := SimulateChainBuild(treeOf(t, base.Hash), []Commit{conflictA, conflictB})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if failedAt != 1 {
		t.Fatalf("failedAt = %d, want 1 (conflictB is the first to fail, replayed after conflictA)", failedAt)
	}
}

func TestGitVersionParsing(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantMaj int
		wantMin int
		wantPat int
	}{
		{"plain", "git version 2.55.0", 2, 55, 0},
		{"old", "git version 2.30.2", 2, 30, 2},
		{"windows suffix", "git version 2.30.2.windows.1", 2, 30, 2},
		{"apple build", "git version 2.39.3 (Apple Git-146)", 2, 39, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token := strings.TrimPrefix(strings.TrimSpace(c.token), "git version ")
			m := gitVersionPattern.FindStringSubmatch(token)
			if m == nil {
				t.Fatalf("gitVersionPattern did not match %q", token)
			}
			major, minor, patch := atoiOrFatal(t, m[1]), atoiOrFatal(t, m[2]), atoiOrFatal(t, m[3])
			if major != c.wantMaj || minor != c.wantMin || patch != c.wantPat {
				t.Fatalf("parsed (%d, %d, %d), want (%d, %d, %d)", major, minor, patch, c.wantMaj, c.wantMin, c.wantPat)
			}
		})
	}
}

func atoiOrFatal(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("strconv.Atoi(%q): %v", s, err)
	}
	return n
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		major, minor, patch             int
		wantMajor, wantMinor, wantPatch int
		want                            bool
	}{
		{2, 55, 0, 2, 38, 0, true},
		{2, 38, 0, 2, 38, 0, true},
		{2, 38, 1, 2, 38, 0, true},
		{2, 37, 9, 2, 38, 0, false},
		{1, 99, 99, 2, 38, 0, false},
		{3, 0, 0, 2, 38, 0, true},
	}
	for _, c := range cases {
		got := atLeast(c.major, c.minor, c.patch, c.wantMajor, c.wantMinor, c.wantPatch)
		if got != c.want {
			t.Fatalf("atLeast(%d.%d.%d, %d.%d.%d) = %v, want %v",
				c.major, c.minor, c.patch, c.wantMajor, c.wantMinor, c.wantPatch, got, c.want)
		}
	}
}

// TestBuildTrailerStrippedChainIsDeterministic: the pushed chain must depend
// only on content and the original commits' identity, so an unchanged group
// isn't re-pushed with new SHAs on every sync — including after the local
// commit was rewritten (e.g. by move/rebase) with only a new committer date.
func TestBuildTrailerStrippedChainIsDeterministic(t *testing.T) {
	t.Chdir(t.TempDir())
	runGitT(t, "init", "-q", "-b", "main")
	runGitT(t, "config", "user.name", "git-sd-test")
	runGitT(t, "config", "user.email", "git-sd-test@example.com")
	commit := func(file, content, msg string, env ...string) Commit {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		runGitT(t, "add", file)
		cmd := exec.Command("git", "commit", "-q", "-m", msg)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit failed: %v\n%s", err, out)
		}
		hash := runGitT(t, "rev-parse", "HEAD")
		return Commit{Hash: hash, Message: runGitT(t, "log", "-1", "--format=%B", hash) + "\n"}
	}
	base := commit("base.txt", "base\n", "base")
	authored := []string{"GIT_AUTHOR_NAME=Original Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z"}
	a := commit("a.txt", "a\n", "Add a\n\ngit-sd-id: saaaa", authored...)
	b := commit("b.txt", "b\n", "Add b\n\nBody.\n\ngit-sd-id: sbbbb", authored...)

	tip1, err := BuildTrailerStrippedChain(base.Hash, []Commit{a, b})
	if err != nil {
		t.Fatal(err)
	}
	tip2, err := BuildTrailerStrippedChain(base.Hash, []Commit{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if tip1 != tip2 {
		t.Fatalf("rebuilding the same chain gave different tips: %s vs %s", tip1, tip2)
	}
	if head := runGitT(t, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Fatalf("original checkout not restored, HEAD is %s", head)
	}

	// Rewrite a and b locally with a new committer date only (as a
	// move/rebase cherry-pick would): the pushed chain must not change.
	runGitT(t, "reset", "-q", "--hard", base.Hash)
	for _, c := range []Commit{a, b} {
		cmd := exec.Command("git", "cherry-pick", c.Hash)
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-09-30T12:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cherry-pick failed: %v\n%s", err, out)
		}
	}
	a2 := Commit{Hash: runGitT(t, "rev-parse", "HEAD~1"), Message: a.Message}
	b2 := Commit{Hash: runGitT(t, "rev-parse", "HEAD"), Message: b.Message}
	if a2.Hash == a.Hash {
		t.Fatal("test setup: rewritten commit kept its hash")
	}
	tip3, err := BuildTrailerStrippedChain(base.Hash, []Commit{a2, b2})
	if err != nil {
		t.Fatal(err)
	}
	if tip3 != tip1 {
		t.Fatalf("committer-date-only rewrite changed the pushed chain: %s vs %s", tip3, tip1)
	}

	if msg := runGitT(t, "log", "-1", "--format=%B", tip1); strings.Contains(msg, "git-sd-id") || msg != "Add b\n\nBody." {
		t.Fatalf("pushed message = %q, want trailer stripped and body kept", msg)
	}
	// %at (Unix time) rather than %aI: older git renders UTC as "+00:00", newer as "Z".
	if author := runGitT(t, "log", "-1", "--format=%an <%ae> %at", tip1); author != "Original Author <author@example.com> 1767323045" {
		t.Fatalf("pushed author = %q, want the original author preserved", author)
	}

	// A real content change must change the tip.
	runGitT(t, "reset", "-q", "--hard", base.Hash)
	changed := commit("a.txt", "a changed\n", "Add a\n\ngit-sd-id: saaaa", authored...)
	tip4, err := BuildTrailerStrippedChain(base.Hash, []Commit{changed})
	if err != nil {
		t.Fatal(err)
	}
	if tip4 == runGitT(t, "rev-parse", tip1+"~1") {
		t.Fatal("a content change produced the same pushed commit")
	}
}
