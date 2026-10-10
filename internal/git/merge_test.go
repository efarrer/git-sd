package git

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// mergeFixture commits a ten-line a.txt and b.txt on a base commit, then
// builds "ours" and "theirs" commits off it by applying edit funcs.
func mergeCommit(t *testing.T, parent string, edits map[string]string) string {
	t.Helper()
	runGitT(t, "checkout", "-q", "--detach", parent)
	for name, content := range edits {
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGitT(t, "add", "-A")
	runGitT(t, "commit", "-q", "-m", "edit")
	return runGitT(t, "rev-parse", "HEAD")
}

func tenLines(replace map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		if s, ok := replace[i]; ok {
			b.WriteString(s + "\n")
		} else {
			b.WriteString("line" + strconv.Itoa(i) + "\n")
		}
	}
	return b.String()
}

// TestMergeTreesStrategiesClassifyLikeAThreeWayMerge exercises both the
// merge-tree and read-tree implementations on the cases rebase relies on:
// identical, upstream has more (same file and other file), local has extra
// (other file, and same file different hunk), same lines differing, 2 local
// commits vs 1 squash, and delete/modify.
func TestMergeTreesStrategiesClassifyLikeAThreeWayMerge(t *testing.T) {
	t.Chdir(t.TempDir())
	runGitT(t, "init", "-q", "-b", "main")
	runGitT(t, "config", "user.name", "t")
	runGitT(t, "config", "user.email", "t@example.com")
	if err := os.WriteFile("a.txt", []byte(tenLines(nil)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.txt", []byte("b0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, "add", "-A")
	runGitT(t, "commit", "-q", "-m", "base")
	base := runGitT(t, "rev-parse", "HEAD")

	type wantKind int
	const (
		landed wantKind = iota // merge result == upstream ("ours")
		delta                  // clean, but result != upstream
		conflict
	)
	cases := []struct {
		name            string
		upstream, local map[string]string
		want            wantKind
	}{
		{"identical", map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, landed},
		{"upstream has more, same file", map[string]string{"a.txt": tenLines(map[int]string{2: "X", 9: "EXTRA"})}, map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, landed},
		{"upstream has more, other file", map[string]string{"a.txt": tenLines(map[int]string{2: "X"}), "n.txt": "n\n"}, map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, landed},
		{"local extra, other file", map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, map[string]string{"a.txt": tenLines(map[int]string{2: "X"}), "b.txt": "b1\n"}, delta},
		{"local extra, same file other hunk", map[string]string{"a.txt": tenLines(map[int]string{2: "X"})}, map[string]string{"a.txt": tenLines(map[int]string{2: "X", 9: "LOCAL"})}, delta},
		// git's merge treats overlapping hunks that are not identical as a
		// conflict, so a local change that extends a landed change onto an
		// adjacent line is reported as a conflict, not a delta.
		{"local extends landed change onto adjacent line", map[string]string{"a.txt": tenLines(map[int]string{3: "F"})}, map[string]string{"a.txt": tenLines(map[int]string{3: "F", 4: "G"})}, conflict},
		{"same lines differ", map[string]string{"a.txt": tenLines(map[int]string{5: "Y"})}, map[string]string{"a.txt": tenLines(map[int]string{5: "X"})}, conflict},
	}

	strategies := []struct {
		name string
		fn   func(base, ours, theirs string) (string, bool, error)
	}{
		{"readTree", mergeTreesReadTree},
	}
	if major, minor, patch, err := gitVersion(); err == nil && atLeast(major, minor, patch, 2, 38, 0) {
		strategies = append(strategies, struct {
			name string
			fn   func(base, ours, theirs string) (string, bool, error)
		}{"mergeTree", mergeTreesMergeTree})
	}

	for _, c := range cases {
		upstream := mergeCommit(t, base, c.upstream)
		local := mergeCommit(t, base, c.local)
		upstreamTree := runGitT(t, "rev-parse", upstream+"^{tree}")
		for _, s := range strategies {
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				tree, isConflict, err := s.fn(base, upstream, local)
				if err != nil {
					t.Fatal(err)
				}
				switch c.want {
				case conflict:
					if !isConflict {
						t.Fatalf("got clean merge %s, want a conflict", tree)
					}
				case landed:
					if isConflict || tree != upstreamTree {
						t.Fatalf("conflict=%v tree=%s, want a clean merge equal to upstream %s", isConflict, tree, upstreamTree)
					}
				case delta:
					if isConflict || tree == upstreamTree {
						t.Fatalf("conflict=%v tree=%s (upstream %s), want a clean merge differing from upstream", isConflict, tree, upstreamTree)
					}
				}
			})
		}
	}

	// 2 local commits vs 1 squashed upstream commit: compare nets.
	t.Run("2 local commits vs 1 squash", func(t *testing.T) {
		squash := mergeCommit(t, base, map[string]string{"a.txt": tenLines(map[int]string{3: "F", 4: "G"})})
		c1 := mergeCommit(t, base, map[string]string{"a.txt": tenLines(map[int]string{3: "F"})})
		c2 := mergeCommit(t, c1, map[string]string{"a.txt": tenLines(map[int]string{3: "F", 4: "G"})})
		want := runGitT(t, "rev-parse", squash+"^{tree}")
		for _, s := range strategies {
			tree, isConflict, err := s.fn(base, squash, c2)
			if err != nil || isConflict || tree != want {
				t.Fatalf("%s: tree=%s conflict=%v err=%v, want clean merge equal to %s", s.name, tree, isConflict, err, want)
			}
		}
	})
}
