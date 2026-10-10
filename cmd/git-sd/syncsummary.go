package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/metadata"
)

// syncAction is what sync did for one PR.
type syncAction int

const (
	syncUnchanged syncAction = iota // nothing needed doing on GitHub
	syncCreated                     // a new PR was opened
	syncUpdated                     // its branch changed, or its title/body/base was edited
)

// syncResult is one PR's outcome in a sync run.
type syncResult struct {
	Action   syncAction
	Number   int
	URL      string
	Title    string
	StackSet string
}

// prFingerprint identifies a PR's title, body and base. Each part is
// length-prefixed so ("a", "b|c") and ("a|b", "c") can never collide.
func prFingerprint(title, body, base string) string {
	h := sha256.New()
	for _, part := range []string{title, body, base} {
		fmt.Fprintf(h, "%d:%s|", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// commitFingerprint identifies a commit as sync pushed it: its patch-id (the
// diff, stable across rebases that leave it alone) and its message with the
// git-sd-id trailer stripped (the message that ships in the PR branch). Parts
// are length-prefixed so they can never collide.
func commitFingerprint(patchID, message string) string {
	h := sha256.New()
	for _, part := range []string{patchID, git.StripTrailer(message, "git-sd-id")} {
		fmt.Fprintf(h, "%d:%s|", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// recordSynced notes, for every commit of group g, that it went into PR
// prNumber as it is now (see commitFingerprint). patchIDs may be nil, in which
// case nothing is recorded.
func recordSynced(m *metadata.Metadata, g PRGroup, prNumber int, patchIDs map[string]string) {
	if patchIDs == nil {
		return
	}
	for _, c := range g.Commits {
		id := git.ExtractTrailer(c.Message, "git-sd-id")
		cm, ok := m.Commits[id]
		if id == "" || !ok {
			continue
		}
		cm.SyncedFingerprint = commitFingerprint(patchIDs[c.Hash], c.Message)
		cm.SyncedPR = prNumber
		m.Commits[id] = cm
	}
}

// needsPREdit reports whether the PR's title, body or base differ from what
// was last successfully applied (lastSynced). An empty lastSynced — a PR from
// before fingerprints were recorded — always needs one edit, which then
// establishes the baseline.
func needsPREdit(lastSynced, title, body, base string) bool {
	return lastSynced == "" || lastSynced != prFingerprint(title, body, base)
}

// formatSyncSummary returns the lines sync prints after its passes: one line
// per PR created or updated (in order), then how many were left unchanged.
func formatSyncSummary(results []syncResult) []string {
	var lines []string
	unchanged := 0
	for _, r := range results {
		switch r.Action {
		case syncCreated:
			lines = append(lines, fmt.Sprintf("Created PR #%d  [%s]  %s  %s", r.Number, r.StackSet, r.Title, r.URL))
		case syncUpdated:
			lines = append(lines, fmt.Sprintf("Updated PR #%d  [%s]  %s  %s", r.Number, r.StackSet, r.Title, r.URL))
		default:
			unchanged++
		}
	}
	switch unchanged {
	case 0:
	case 1:
		lines = append(lines, "1 PR unchanged.")
	default:
		lines = append(lines, fmt.Sprintf("%d PRs unchanged.", unchanged))
	}
	return lines
}
