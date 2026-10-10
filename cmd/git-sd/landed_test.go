package main

import (
	"git-sd/internal/git"
	"git-sd/internal/github"
	"git-sd/internal/metadata"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

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

// landedFixture builds a local group of two NON-contiguous commits (an
// unrelated commit sits between them, as with interleaved Stack Sets or a
// sub-commit): c1 sets line 3 to F, c2 sets line 7 to G.
type landedFixture struct {
	base   string
	c1, c2 git.Commit
	g      PRGroup
}

func newLandedFixture(t *testing.T) *landedFixture {
	t.Helper()
	initRepo(t)
	b := commitFile(t, "big.txt", tenLines(nil), "big file")
	f := &landedFixture{base: b.Hash}
	gitT(t, "checkout", "-q", "-b", "work", f.base)
	f.c1 = trackedCommit(t, "big.txt", tenLines(map[int]string{3: "F"}), "c1 sets F", "saaaa")
	commitFile(t, "u.txt", "unrelated\n", "u")
	f.c2 = trackedCommit(t, "big.txt", tenLines(map[int]string{3: "F", 7: "G"}), "c2 sets G", "sbbbb")
	f.g = PRGroup{StackSet: "set", TopLevelID: "saaaa", Commits: []git.Commit{f.c1, f.c2}}
	return f
}

// upstream makes an "upstream" commit off base with the given file content and
// returns its hash; later adds one more unrelated commit on top and returns
// that tip.
func (f *landedFixture) upstream(t *testing.T, name, content string) (mc, tip string) {
	t.Helper()
	gitT(t, "checkout", "-q", "-b", name, f.base)
	mc = commitFile(t, "big.txt", content, "upstream "+name).Hash
	commitFile(t, "later.txt", "later "+name+"\n", "later "+name)
	return mc, gitT(t, "rev-parse", "HEAD")
}

func TestClassifyLanded(t *testing.T) {
	f := newLandedFixture(t)

	squashMC, squashTip := f.upstream(t, "squash", tenLines(map[int]string{3: "F", 7: "G"}))
	moreMC, moreTip := f.upstream(t, "more", tenLines(map[int]string{3: "F", 7: "G", 9: "EXTRA"}))
	deltaMC, deltaTip := f.upstream(t, "delta", tenLines(map[int]string{3: "F"}))
	conflictMC, conflictTip := f.upstream(t, "conflict", tenLines(map[int]string{3: "F", 7: "DIFFERENT"}))

	// Rebase-merge: the group's commits replayed one by one.
	gitT(t, "checkout", "-q", "-b", "rebased", f.base)
	commitFile(t, "big.txt", tenLines(map[int]string{3: "F"}), "r1")
	rebasedMC := commitFile(t, "big.txt", tenLines(map[int]string{3: "F", 7: "G"}), "r2").Hash

	// Merge commit: a PR branch holding just the group, merged --no-ff with
	// concurrent upstream work on the other side.
	gitT(t, "checkout", "-q", "-b", "pr", f.base)
	gitT(t, "cherry-pick", f.c1.Hash, f.c2.Hash)
	gitT(t, "checkout", "-q", "-b", "merged", f.base)
	commitFile(t, "main-side.txt", "concurrent\n", "concurrent upstream work")
	gitT(t, "merge", "-q", "--no-ff", "-m", "merge pr", "pr")
	mergeMC := gitT(t, "rev-parse", "HEAD")

	cases := []struct {
		name    string
		info    *github.PRInfo
		baseTip string
		want    landedKind
		wantErr string
	}{
		{"squash", mergedInfo("main", squashMC), squashTip, landedFully, ""},
		{"rebase-merge", mergedInfo("main", rebasedMC), rebasedMC, landedFully, ""},
		{"merge commit", mergedInfo("main", mergeMC), mergeMC, landedFully, ""},
		{"upstream has more", mergedInfo("main", moreMC), moreTip, landedFully, ""},
		{"local has extra changes", mergedInfo("main", deltaMC), deltaTip, landedWithDelta, ""},
		{"same lines differ", mergedInfo("main", conflictMC), conflictTip, landedConflicts, ""},
		// A stack-cascaded PR keeps its lower PR's branch as baseRefName even
		// after landing on the base branch; only the merge commit matters.
		{"stack-merged (base ref is a stack branch)", mergedInfo("lower-pr-branch", squashMC), squashTip, landedFully, ""},
		{"merge commit not on base", mergedInfo("main", squashMC), rebasedMC, 0, "is not on"},
		{"no merge commit", &github.PRInfo{State: "MERGED", BaseRefName: "main"}, squashTip, 0, "no merge commit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := classifyLanded(f.g, c.info, "origin/main", c.baseTip)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("classifyLanded = %v, want error containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyLanded = %v, want nil", err)
			}
			if res.Kind != c.want {
				t.Fatalf("kind = %v, want %v", res.Kind, c.want)
			}
		})
	}
}

func TestBuildFollowUp(t *testing.T) {
	f := newLandedFixture(t)
	deltaMC, deltaTip := f.upstream(t, "delta", tenLines(map[int]string{3: "F"}))
	conflictMC, conflictTip := f.upstream(t, "conflict", tenLines(map[int]string{3: "F", 7: "DIFFERENT"}))
	m := &metadata.Metadata{
		Commits: map[string]metadata.CommitMetadata{
			"saaaa": {StackSet: "set", PRNumber: 7},
			"sbbbb": {StackSet: "set", ParentID: "saaaa", SubRank: "1"},
		},
		StackSets: map[string]metadata.StackSetMetadata{},
	}

	t.Run("delta keeps only what did not land", func(t *testing.T) {
		res, err := classifyLanded(f.g, mergedInfo("main", deltaMC), "origin/main", deltaTip)
		if err != nil || res.Kind != landedWithDelta {
			t.Fatalf("res=%+v err=%v, want landedWithDelta", res, err)
		}
		fu, err := buildFollowUp(m, f.g, res)
		if err != nil {
			t.Fatal(err)
		}
		if parent := gitT(t, "rev-parse", fu.Commit.Hash+"^"); parent != deltaMC {
			t.Fatalf("follow-up parent = %s, want the merge commit %s", parent, deltaMC)
		}
		if tree := gitT(t, "rev-parse", fu.Commit.Hash+"^{tree}"); tree != res.ResultTree {
			t.Fatalf("follow-up tree = %s, want the merge result %s", tree, res.ResultTree)
		}
		if files := gitT(t, "diff", "--name-only", deltaMC, fu.Commit.Hash); files != "big.txt" {
			t.Fatalf("follow-up changes %q, want only big.txt", files)
		}
		var changed []string
		for _, l := range strings.Split(gitT(t, "diff", "-U0", deltaMC, fu.Commit.Hash), "\n") {
			if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
				changed = append(changed, l)
			}
		}
		if got := strings.Join(changed, ","); got != "-line7,+G" {
			t.Fatalf("follow-up should change only line 7 (F already landed), changed lines: %s", got)
		}
		msg := gitT(t, "log", "-1", "--format=%B", fu.Commit.Hash)
		if !strings.HasPrefix(msg, "Follow-up to PR #7: c1 sets F") || !strings.Contains(msg, "git-sd-id: "+fu.ID) {
			t.Fatalf("unexpected follow-up message:\n%s", msg)
		}
		if fu.ID == "saaaa" || fu.ID == "sbbbb" {
			t.Fatalf("follow-up reused an old id %s", fu.ID)
		}
		if fu.StackSet != "set" || fu.FromPR != 7 || fu.FirstHash != f.c1.Hash {
			t.Fatalf("unexpected follow-up bookkeeping: %+v", fu)
		}
		if got, want := gitT(t, "log", "-1", "--format=%an", fu.Commit.Hash), gitT(t, "log", "-1", "--format=%an", f.c1.Hash); got != want {
			t.Fatalf("author = %q, want the group's author %q", got, want)
		}
	})

	t.Run("conflict replays the group's net change onto the pre-merge commit", func(t *testing.T) {
		res, err := classifyLanded(f.g, mergedInfo("main", conflictMC), "origin/main", conflictTip)
		if err != nil || res.Kind != landedConflicts {
			t.Fatalf("res=%+v err=%v, want landedConflicts", res, err)
		}
		fu, err := buildFollowUp(m, f.g, res)
		if err != nil {
			t.Fatal(err)
		}
		if parent := gitT(t, "rev-parse", fu.Commit.Hash+"^"); parent != res.Before {
			t.Fatalf("follow-up parent = %s, want the pre-merge commit %s", parent, res.Before)
		}
		if tree := gitT(t, "rev-parse", fu.Commit.Hash+"^{tree}"); tree != res.LocalTree {
			t.Fatalf("follow-up tree = %s, want the local tree %s", tree, res.LocalTree)
		}

		// Replaying it onto the upstream tip conflicts, as a real rebase would.
		gitT(t, "checkout", "-q", "--detach", conflictTip)
		if out, err := gitTErr("cherry-pick", fu.Commit.Hash); err == nil {
			t.Fatalf("expected the follow-up to conflict with upstream, got:\n%s", out)
		}
		gitT(t, "cherry-pick", "--abort")
	})
}

// gitTErr runs git and returns combined output and the error, for cases that
// are expected to fail.
func gitTErr(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return string(out), err
}

// followUpRebase runs rebaseOnto for a merged group (c1, c2) whose PR #7
// landed as upstreamLine7 on line 7, with an unrelated commit u between the
// group's commits and an open commit d after them.
type followUpRebase struct {
	m        *metadata.Metadata
	fu       followUp
	err      error
	origHead string
}

func newFollowUpRebase(t *testing.T, upstreamLines map[int]string) *followUpRebase {
	t.Helper()
	f := newLandedFixture(t)
	tipBranch := "upstream_up"
	mc, tip := f.upstream(t, tipBranch, tenLines(upstreamLines))
	gitT(t, "checkout", "-q", "work")
	d := trackedCommit(t, "d.txt", "d\n", "open d", "sdddd")
	// u is the unrelated commit between c1 and c2; recover it from history.
	var u git.Commit
	for _, c := range mustCommitsInRange(t, f.base) {
		if strings.HasPrefix(c.Message, "u\n") {
			u = c
		}
	}
	commits := []git.Commit{f.c1, u, f.c2, d}

	r := &followUpRebase{origHead: d.Hash}
	r.m = &metadata.Metadata{
		Commits: map[string]metadata.CommitMetadata{
			"saaaa": {StackSet: "set", PRNumber: 7, BranchName: "b-saaaa"},
			"sbbbb": {StackSet: "set", ParentID: "saaaa", SubRank: "1"},
			"sdddd": {StackSet: "set"},
		},
		StackSets: map[string]metadata.StackSetMetadata{},
	}
	res, err := classifyLanded(f.g, mergedInfo("main", mc), "upstream_up", tip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind == landedFully {
		t.Fatal("fixture should not be fully landed")
	}
	r.fu, err = buildFollowUp(r.m, f.g, res)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(r.m)
	if err != nil {
		t.Fatal(err)
	}
	drop := map[string]bool{f.c1.Hash: true, f.c2.Hash: true}
	_, r.err = rebaseOnto(r.m, snap, tipBranch, commits, drop, nil, []followUp{r.fu})
	return r
}

func mustCommitsInRange(t *testing.T, since string) []git.Commit {
	t.Helper()
	cs, err := git.GetCommitsInRange(since)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestRebaseKeepsLocalOnlyChangesAsFollowUpCommit(t *testing.T) {
	// Upstream landed only F (line 3); the group also set line 7 (G), which
	// never landed, so a follow-up commit must carry just that.
	r := newFollowUpRebase(t, map[int]string{3: "F"})
	if r.err != nil {
		t.Fatalf("rebaseOnto: %v", r.err)
	}
	commits := mustCommitsInRange(t, "upstream_up")
	ids := commitIDs(commits)
	if len(ids) != 3 || ids[0] != r.fu.ID || ids[2] != "sdddd" {
		t.Fatalf("branch ids = %v, want [%s <unrelated> sdddd] (follow-up at the group's position)", ids, r.fu.ID)
	}
	if got := gitT(t, "show", "HEAD:big.txt"); !strings.Contains(got, "F") || !strings.Contains(got, "G") {
		t.Fatalf("big.txt after rebase lacks F and G:\n%s", got)
	}
	cm, ok := r.m.Commits[r.fu.ID]
	if !ok || cm.StackSet != "set" || cm.PRNumber != 0 || cm.ParentID != "" {
		t.Fatalf("follow-up not registered as assigned/no PR: %+v ok=%v", cm, ok)
	}
	for _, old := range []string{"saaaa", "sbbbb"} {
		if _, ok := r.m.Commits[old]; ok {
			t.Fatalf("old group commit %s should be retired", old)
		}
	}
	if len(r.m.PendingBranchDeletions) != 1 || r.m.PendingBranchDeletions[0].Branch != "b-saaaa" {
		t.Fatalf("expected the merged PR's branch queued for deletion: %+v", r.m.PendingBranchDeletions)
	}
}

func TestRebaseFollowUpConflictContinueRegistersIt(t *testing.T) {
	// Upstream landed F and a DIFFERENT line 7: the follow-up collides.
	r := newFollowUpRebase(t, map[int]string{3: "F", 7: "DIFFERENT"})
	if r.err == nil || !git.RebaseInProgress() {
		t.Fatalf("expected a conflict with a rebase in progress, err=%v", r.err)
	}
	for _, want := range []string{"Follow-up to PR #7", "big.txt", "WHOLE file", "git rebase --skip"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Fatalf("conflict message missing %q:\n%v", want, r.err)
		}
	}
	resolveFile(t, "big.txt", tenLines(map[int]string{3: "F", 7: "RESOLVED"}))
	m := settle(t)
	if cm, ok := m.Commits[r.fu.ID]; !ok || cm.StackSet != "set" {
		t.Fatalf("follow-up should be registered after --continue: %+v ok=%v", cm, ok)
	}
	if _, ok := m.Commits["saaaa"]; ok {
		t.Fatal("old group should be retired after --continue")
	}
}

func TestRebaseFollowUpConflictSkipTakesUpstream(t *testing.T) {
	r := newFollowUpRebase(t, map[int]string{3: "F", 7: "DIFFERENT"})
	if r.err == nil || !git.RebaseInProgress() {
		t.Fatalf("expected a conflict with a rebase in progress, err=%v", r.err)
	}
	gitT(t, "rebase", "--skip")
	m := settle(t)
	if _, ok := m.Commits[r.fu.ID]; ok {
		t.Fatal("a skipped follow-up must not be registered")
	}
	if _, ok := m.Commits["saaaa"]; ok {
		t.Fatal("the merged group should still be retired after --skip")
	}
	if got := gitT(t, "show", "HEAD:big.txt"); !strings.Contains(got, "DIFFERENT") {
		t.Fatalf("--skip should leave upstream's version:\n%s", got)
	}
}
