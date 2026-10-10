package main

import (
	"errors"
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/github"
	"git-sd/internal/metadata"
	"strings"
)

// landedKind is how a MERGED group's local content relates to what landed.
type landedKind int

const (
	// landedFully: upstream already has everything in the group (identical,
	// or upstream has more, e.g. reviewer edits applied on GitHub).
	landedFully landedKind = iota
	// landedWithDelta: the group has extra local changes that never landed
	// (e.g. it was amended after its last sync) and merge cleanly on top.
	landedWithDelta
	// landedConflicts: the group's local changes collide with what landed.
	landedConflicts
)

type landedResult struct {
	Kind        landedKind
	MergeCommit string
	Before      string // the commit just before the merge, used as the merge base
	LocalTree   string // the group's commits replayed onto Before
	ResultTree  string // landedWithDelta: LocalTree merged onto the landed tree
}

// classifyLanded compares MERGED group g with what its merge landed, by NET
// content — never commit by commit, so 2 local commits against 1 squashed
// upstream commit works, and a group whose commits are not contiguous locally
// (sub-commits, interleaved Stack Sets) is replayed rather than range-diffed.
//
// The group's commits are replayed onto a candidate "before" commit
// (mergeCommit^1 for squash/merge-commit merges, which GitHub's API does not
// distinguish; mergeCommit~N, N = the group's commit count, for rebase-merge).
// If the replayed tree equals the merge commit's tree the group landed
// exactly. Otherwise the first candidate that replays cleanly is used for a
// three-way merge (base = before, ours = landed, theirs = local) to tell
// "upstream has more" from "local has extra changes" from "they collide".
//
// info.BaseRefName is deliberately not checked: in a cascading stack merge,
// every PR above the bottom one keeps its lower PR's branch as its base even
// after landing on the base branch. A PR merged into some other branch fails
// the merge-commit-on-base check instead.
func classifyLanded(g PRGroup, info *github.PRInfo, baseBranch, baseTip string) (landedResult, error) {
	if info.MergeCommit == nil || info.MergeCommit.OID == "" {
		return landedResult{}, errors.New("GitHub reported no merge commit for it")
	}
	mc := info.MergeCommit.OID
	if !git.IsAncestor(mc, baseTip) {
		return landedResult{}, fmt.Errorf("its merge commit %s is not on %s", shortHash(mc), baseBranch)
	}
	afterTree, err := git.RevParse(mc + "^{tree}")
	if err != nil {
		return landedResult{}, err
	}

	candidates := []string{mc + "^1"}
	if n := len(g.Commits); n > 1 {
		candidates = append(candidates, fmt.Sprintf("%s~%d", mc, n))
	}
	var firstBefore, firstTree string
	for _, cand := range candidates {
		before, err := git.RevParse(cand)
		if err != nil {
			continue // e.g. history too shallow for ~N
		}
		tree, failedAt, err := git.SimulateChainTree(before, g.Commits)
		if err != nil {
			return landedResult{}, err
		}
		if failedAt != -1 {
			continue
		}
		if tree == afterTree {
			return landedResult{Kind: landedFully, MergeCommit: mc, Before: before, LocalTree: tree}, nil
		}
		if firstBefore == "" {
			firstBefore, firstTree = before, tree
		}
	}
	if firstBefore == "" {
		return landedResult{}, errors.New("its commits can't be replayed onto the code just before its merge, so they can't be compared with what landed")
	}

	author, err := git.GetAuthor(g.Commits[0].Hash)
	if err != nil {
		return landedResult{}, err
	}
	localCommit, err := git.CreateCommitAs(firstTree, firstBefore, "git-sd: local content of merged group", author)
	if err != nil {
		return landedResult{}, err
	}
	res := landedResult{MergeCommit: mc, Before: firstBefore, LocalTree: firstTree}
	merged, conflict, err := git.MergeTrees(firstBefore, mc, localCommit)
	switch {
	case err != nil:
		return landedResult{}, err
	case conflict:
		res.Kind = landedConflicts
	case merged == afterTree:
		res.Kind = landedFully
	default:
		res.Kind = landedWithDelta
		res.ResultTree = merged
	}
	return res, nil
}

// followUp is a synthetic commit that stands in for a merged group's local
// changes that did not land. It is picked at the position of the group's
// first commit in the rebase todo.
type followUp struct {
	metadata.PendingSynthetic
	Commit    git.Commit
	FirstHash string // hash of the group's first commit
}

// buildFollowUp creates the follow-up commit for a delta or conflict group.
// Delta: its tree is the merge result and its parent the merge commit, so its
// diff is exactly the delta. Conflict: its tree is the group's local tree and
// its parent the pre-merge commit, so replaying it onto the new base surfaces
// the real conflict.
func buildFollowUp(m *metadata.Metadata, g PRGroup, res landedResult) (followUp, error) {
	top := g.Commits[0]
	author, err := git.GetAuthor(top.Hash)
	if err != nil {
		return followUp{}, err
	}
	topSubject, _, _ := strings.Cut(strings.TrimSpace(stripTrailers(top.Message)), "\n")
	topCM := m.Commits[g.TopLevelID]

	var body strings.Builder
	fmt.Fprintf(&body, "Follow-up to PR #%d: %s\n\nLocal changes from this group that had not landed when PR #%d merged. Original commits:\n", topCM.PRNumber, topSubject, topCM.PRNumber)
	for _, c := range g.Commits {
		subject, _, _ := strings.Cut(strings.TrimSpace(stripTrailers(c.Message)), "\n")
		fmt.Fprintf(&body, "- %s %s\n", shortHash(c.Hash), subject)
	}
	id := git.GenerateID()
	message, err := git.AddTrailer(body.String(), id)
	if err != nil {
		return followUp{}, err
	}

	tree, parent := res.ResultTree, res.MergeCommit
	if res.Kind == landedConflicts {
		tree, parent = res.LocalTree, res.Before
	}
	hash, err := git.CreateCommitAs(tree, parent, message, author)
	if err != nil {
		return followUp{}, err
	}
	return followUp{
		PendingSynthetic: metadata.PendingSynthetic{ID: id, StackSet: topCM.StackSet, FromPR: topCM.PRNumber},
		Commit:           git.Commit{Hash: hash, Message: message},
		FirstHash:        top.Hash,
	}, nil
}

// stripTrailers removes the git-sd-id trailer line from a message, for display.
func stripTrailers(msg string) string {
	var out []string
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(l, "git-sd-id:") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
