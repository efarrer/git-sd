package main

import (
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/metadata"
	"strconv"
	"strings"
)

// rankLess reports whether rank a sorts before rank b, comparing them as the
// fractional values they represent (e.g. "1" < "15" < "2" < "9" < "91"),
// by right-padding the shorter digit string with "0" to equal length and
// comparing as integers. See DESIGN.md's "Metadata Schema" section.
func rankLess(a, b string) bool {
	for len(a) < len(b) {
		a += "0"
	}
	for len(b) < len(a) {
		b += "0"
	}
	// Both are digit strings of equal length now; compare numerically
	// (not lexicographically — same length makes them equivalent here,
	// but strconv keeps this correct if that ever changes).
	av, _ := strconv.Atoi(a)
	bv, _ := strconv.Atoi(b)
	return av < bv
}

// nextRank computes the next auto-assigned rank after the given existing
// sibling ranks (empty if there are none yet). See DESIGN.md's "Metadata Schema"
// "Auto-rank algorithm": within the current digit-length tier, increment by 1; once
// that tier is exhausted (all-9s), move to the next tier.
func nextRank(existing []string) string {
	max := "0"
	for _, r := range existing {
		if rankLess(max, r) {
			max = r
		}
	}
	if max == "0" {
		return "1"
	}
	allNines := strings.Count(max, "9") == len(max)
	if !allNines {
		v, _ := strconv.Atoi(max)
		return fmt.Sprintf("%0*d", len(max), v+1)
	}
	// Tier exhausted: pad with a trailing "0" and add 1 in the wider tier.
	widened := max + "0"
	v, _ := strconv.Atoi(widened)
	return fmt.Sprintf("%0*d", len(widened), v+1)
}

// parseAddress splits a "<idx>" or "<idx>.<rank>" address into its parts.
// rank is "" for a bare top-level index.
func parseAddress(s string) (idx int, rank string, err error) {
	idxPart, rankPart, hasDot := strings.Cut(s, ".")
	idx, err = strconv.Atoi(idxPart)
	if err != nil {
		return 0, "", fmt.Errorf("invalid index %q", idxPart)
	}
	if !hasDot {
		return idx, "", nil
	}
	if rankPart == "" {
		return 0, "", fmt.Errorf("invalid sub-commit rank in %q", s)
	}
	return idx, rankPart, nil
}

// resolveExisting resolves a parsed address to its physical position in
// commits and its git-sd-id. A bare index just resolves to commits[idx]; an
// "idx.rank" address finds idx's own commit (the parent), then scans commits
// for the one whose metadata has ParentID equal to that parent's id and
// SubRank equal to rank.
func resolveExisting(commits []git.Commit, m *metadata.Metadata, idx int, rank string) (physIdx int, id string, err error) {
	if idx < 0 || idx >= len(commits) {
		return -1, "", fmt.Errorf("index %d out of range", idx)
	}
	if rank == "" {
		return idx, git.ExtractTrailer(commits[idx].Message, "git-sd-id"), nil
	}
	parentID := git.ExtractTrailer(commits[idx].Message, "git-sd-id")
	for i, c := range commits {
		cid := git.ExtractTrailer(c.Message, "git-sd-id")
		if cid == "" {
			continue
		}
		cm := m.Commits[cid]
		if cm.ParentID == parentID && cm.SubRank == rank {
			return i, cid, nil
		}
	}
	return -1, "", fmt.Errorf("no sub-commit %d.%s found", idx, rank)
}
