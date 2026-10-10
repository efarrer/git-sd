package main

import (
	"git-sd/internal/git"
	"git-sd/internal/metadata"
)

// PRGroup is one PR's worth of commits: a top-level commit plus any
// sub-commits (PR-feedback fixes) grouped under it. The group's identity —
// and therefore its branch name — is the top-level commit's git-sd-id, which
// is stable across amend/rebase/reorder/partial-merge. Only the top-level
// commit carries PR metadata; sub-commits inherit the group's PR.
type PRGroup struct {
	StackSet   string
	TopLevelID string       // git-sd-id of the top-level commit (branch identity)
	Commits    []git.Commit // top-level commit followed by its sub-commits, in physical order
}

// Tip returns the last commit of the group — the commit its branch points at
// (so the branch contains the top-level commit and all its sub-commits).
func (g PRGroup) Tip() git.Commit {
	return g.Commits[len(g.Commits)-1]
}

// GroupCommits walks commits in physical order (the source of truth for
// order) and folds them into PR groups, keyed by each commit's explicit
// ParentID (see DESIGN.md's "Metadata Schema") rather than physical adjacency:
//   - A top-level commit (ParentID=="") assigned to a Stack Set
//     (StackSet!="") opens a new group.
//   - A sub-commit (ParentID!="") attaches to its parent's group, if that
//     parent has one open — i.e. the parent is itself assigned to a Stack
//     Set. A sub-commit whose parent isn't assigned (dormant) or whose
//     ParentID doesn't resolve to any tracked commit (stale/orphaned) is
//     excluded entirely — never silently becomes its own group.
//
// Groups are returned in the order their top-level commits appear.
func GroupCommits(commits []git.Commit, m *metadata.Metadata) []PRGroup {
	var groups []PRGroup
	groupIndex := map[string]int{} // top-level id -> index into groups

	for _, c := range commits {
		id := git.ExtractTrailer(c.Message, "git-sd-id")
		cm := m.Commits[id]

		if cm.ParentID == "" {
			if cm.StackSet == "" {
				continue
			}
			groups = append(groups, PRGroup{
				StackSet:   cm.StackSet,
				TopLevelID: id,
				Commits:    []git.Commit{c},
			})
			groupIndex[id] = len(groups) - 1
			continue
		}

		gi, ok := groupIndex[cm.ParentID]
		if !ok {
			continue
		}
		groups[gi].Commits = append(groups[gi].Commits, c)
	}

	return groups
}
