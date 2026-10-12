package main

import (
	"errors"
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/github"
	"git-sd/internal/metadata"
	"git-sd/internal/timing"
	"git-sd/internal/ui"
	"os"
	"strings"
)

// maxConcurrentPRLookups bounds how many PR lookups status runs at once: enough
// that a typical stack costs about one round trip, few enough to stay clear of
// GitHub's secondary rate limits.
const maxConcurrentPRLookups = 8

// getDetailedPRInfo is github.GetDetailedPRInfo; tests replace it.
var getDetailedPRInfo = github.GetDetailedPRInfo

// prLookup is the outcome of looking up one PR.
type prLookup struct {
	info *github.DetailedPRInfo
	err  error
}

// fetchPRInfos looks up every PR in numbers concurrently, at most
// maxConcurrentPRLookups at a time, and returns the outcomes keyed by number.
func fetchPRInfos(numbers []int) map[int]prLookup {
	return fetchConcurrently(numbers, func(n int) prLookup {
		info, err := getDetailedPRInfo(n)
		return prLookup{info: info, err: err}
	})
}

func runStatus(args []string) {
	// The legend needs nothing from the repository, so print it before any
	// lookup that can fail or return early (no base branch, no commits).
	if legendRequested(args, git.ConfigGetWithDefault("sd.status.legend", "false")) {
		ui.PrintLegend()
		fmt.Println()
	}

	columns, err := statusColumns(git.ConfigGetWithDefault("sd.status.columns", defaultStatusColumns))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
	op := activeOperation() // a sync or merge running right now, if any
	color := ui.ColorEnabled(git.ConfigGetWithDefault("sd.status.color", "true"), os.Getenv, stdoutIsTerminal())

	view, err := buildStatusView(statusViewOptions{Columns: columns, Color: color, Op: op, RefreshIndex: true})
	if err != nil {
		var failure *statusFailure
		if errors.As(err, &failure) {
			fmt.Println(failure.line)
		} else {
			fmt.Printf("Error: %v\n", err)
		}
		exitCleanly(1)
	}
	if view.NoCommits {
		if op != nil {
			// sync briefly checks out the base commit while it builds each PR's branch.
			fmt.Printf("Note: a git sd %s is running and has the branch temporarily rearranged; run status again in a moment.\n", op.Command)
		}
		fmt.Printf("No local commits found since %s.\n", view.BaseBranch)
		return
	}
	for _, w := range view.Warnings {
		fmt.Fprintln(os.Stderr, w)
	}

	// A running sync or merge will save the metadata itself, and a save from
	// here, made from what was loaded earlier, could overwrite what it records.
	if op == nil {
		if err := view.Metadata.Save(); err != nil {
			fmt.Printf("Error saving metadata: %v\n", err)
		}
	}

	ui.PrintTable(view.Header, view.Rows)
	for _, w := range view.Failures.warnings() {
		fmt.Fprintln(os.Stderr, w)
	}
}

// statusFailure is an error from buildStatusView that carries the exact line
// `status` prints for it (the cause alone is what `watch` reports).
type statusFailure struct {
	line  string
	cause error
}

func (e *statusFailure) Error() string { return e.cause.Error() }
func (e *statusFailure) Unwrap() error { return e.cause }

// statusViewOptions say how to build the view of the branch.
type statusViewOptions struct {
	Columns      []string
	Color        bool
	Op           *operation // a sync or merge running right now, if any
	RefreshIndex bool       // update the recorded index of entries that exist (status does; watch must not touch metadata)
}

// statusView is everything `status` shows for one moment: the table, the PRs it
// covers with their GitHub-derived state (what `watch` follows), and what to
// say after the table. Building it makes exactly one lookup per PR.
type statusView struct {
	Header []string
	Rows   [][]string

	PRs            []watchedPR // top-level groups that have a PR, in stack order
	LookupsTotal   int         // how many PRs were looked up
	LookupsFailed  int         // ... and how many of those lookups failed
	FirstLookupErr error

	Failures lookupFailures // for the warnings printed after the table
	Warnings []string       // problems building the view (stderr)
	Metadata *metadata.Metadata

	NoCommits  bool // nothing in range: there is no table
	BaseBranch string
}

// buildStatusView reads the branch, the metadata and GitHub and builds the
// status table. It writes nothing: saving the metadata is the caller's job.
func buildStatusView(opts statusViewOptions) (*statusView, error) {
	columns, color, op := opts.Columns, opts.Color, opts.Op
	done := timing.Phase("resolve base branch")
	baseBranch, _, since, err := git.ResolveBaseBranch()
	done()
	if err != nil {
		return nil, &statusFailure{line: fmt.Sprintf("Error: %v", err), cause: err}
	}

	done = timing.Phase("read commits")
	commits, err := git.GetCommitsInRange(since)
	done()
	if err != nil {
		return nil, &statusFailure{line: fmt.Sprintf("Error getting commits: %v", err), cause: err}
	}

	if len(commits) == 0 {
		return &statusView{NoCommits: true, BaseBranch: baseBranch}, nil
	}

	done = timing.Phase("load metadata")
	m, err := metadata.Load()
	done()
	if err != nil {
		return nil, &statusFailure{line: fmt.Sprintf("Error loading metadata: %v", err), cause: err}
	}

	view := &statusView{Header: statusHeader(columns), Metadata: m, BaseBranch: baseBranch}

	// Precompute each commit's current physical index, so a sub-commit's
	// label can show its PARENT's index (see SPEC.md's "Hierarchical Indexing"), not its own.
	done = timing.Phase("index trailers")
	physIdx := map[string]int{}
	for i, c := range commits {
		if id := git.ExtractTrailer(c.Message, "git-sd-id"); id != "" {
			physIdx[id] = i
		}
	}

	// Look up every assigned commit's PR up front, concurrently, so the cost
	// is about one round trip however many PRs the stack has.
	var prNumbers []int
	seenPR := map[int]bool{}
	for _, c := range commits {
		id := git.ExtractTrailer(c.Message, "git-sd-id")
		if cm, ok := m.Commits[id]; ok && id != "" && cm.StackSet != "" && cm.PRNumber != 0 && !seenPR[cm.PRNumber] {
			seenPR[cm.PRNumber] = true
			prNumbers = append(prNumbers, cm.PRNumber)
		}
	}
	done()
	done = timing.Phase("fetch PR states")
	prInfo := fetchPRInfos(prNumbers)
	done()

	// The PRs themselves (for watch): one per top-level group that has a PR,
	// with its place among its Stack Set's groups and the state GitHub reports.
	groups := GroupCommits(commits, m)
	setSize := map[string]int{} // every group counts, so the place matches the order status shows
	for _, g := range groups {
		setSize[g.StackSet]++
	}
	placed := map[string]int{}
	for _, g := range groups {
		placed[g.StackSet]++
		cm := m.Commits[g.TopLevelID]
		if cm.PRNumber == 0 {
			continue
		}
		title, _, _ := strings.Cut(g.Commits[0].Message, "\n")
		p := watchedPR{Number: cm.PRNumber, URL: cm.PRURL, StackSet: g.StackSet, Title: title,
			Position: placed[g.StackSet], SetSize: setSize[g.StackSet]}
		view.LookupsTotal++
		if lk, ok := prInfo[cm.PRNumber]; ok && lk.err == nil && lk.info != nil {
			p.Marker = classifyPR(lk.info)
		} else {
			view.LookupsFailed++
			if view.FirstLookupErr == nil && ok {
				view.FirstLookupErr = lk.err
			}
		}
		view.PRs = append(view.PRs, p)
	}

	// Whether a sub-commit has reached its PR is judged by patch-id; compute
	// them (one pipeline) only if some sub-commit's parent has a PR.
	done = timing.Phase("patch-ids")
	var patchIDs map[string]string
	var patchErr error
	for _, c := range commits {
		id := git.ExtractTrailer(c.Message, "git-sd-id")
		if cm, ok := m.Commits[id]; ok && cm.ParentID != "" {
			if parent, ok := m.Commits[cm.ParentID]; ok && parent.PRNumber != 0 {
				patchIDs, patchErr = git.PatchIDs(since)
				break
			}
		}
	}
	done()
	if patchErr != nil {
		view.Warnings = append(view.Warnings, fmt.Sprintf("Warning: could not check which sub-commits are in their PRs: %v", patchErr))
	}

	// A failed lookup is shown as unavailable, never guessed; say why once,
	// after the table (see lookupFailures).
	view.Failures = summarizeLookups(prNumbers, prInfo)

	// We'll track the order for metadata synchronization
	done = timing.Phase("build rows")
	defer func() { done() }()
	for i, c := range commits {
		id := git.ExtractTrailer(c.Message, "git-sd-id")

		firstLine, _, _ := strings.Cut(c.Message, "\n")
		if len(firstLine) > 50 {
			firstLine = firstLine[:47] + "..."
		}

		if id == "" {
			// No git-sd-id yet: this commit hasn't been tracked, so there's no
			// metadata to sync or read. Run 'git sd track' to assign one.
			untracked := ui.MarkerFor(ui.Untracked)
			view.Rows = append(view.Rows, statusRow(columns, map[string]string{
				"Icon":    untracked.Icon,
				"Status":  statusWord(untracked, color),
				"Index":   fmt.Sprintf("%d", i),
				"Hash":    c.Hash[:7],
				"Message": firstLine,
			}))
			continue
		}

		cm, ok := m.Commits[id]
		if !ok {
			cm = metadata.CommitMetadata{}
		}

		// Refresh the recorded index of an entry that exists. A commit with no
		// entry (tracked but not assigned) is Ready and records nothing, so
		// status never creates an entry just to hold an index (B-022).
		if ok && opts.RefreshIndex {
			cm.Index = i
			m.Commits[id] = cm
		}

		var icon, word string
		if cm.ParentID == "" {
			marker := ui.MarkerFor(withOperation(markerID(true, cm, prInfo), op.affects(cm.StackSet)))
			icon, word = marker.Icon, statusWordWith(marker, ciProgress(marker.ID, prInfo, cm.PRNumber), color)
		} else {
			// A sub-commit is part of its parent's PR: no state of its own, only
			// whether it has reached that PR yet.
			firstLine = "└─ " + firstLine
			if parent, ok := m.Commits[cm.ParentID]; ok && physIdxHas(physIdx, cm.ParentID) && patchErr == nil {
				if id, unsynced := subCommitMarker(cm, parent, markerID(true, parent, prInfo), commitFingerprint(patchIDs[c.Hash], c.Message)); unsynced {
					marker := ui.MarkerFor(id)
					icon, word = marker.Icon, statusWord(marker, color)
				}
			}
		}

		indexStr := fmt.Sprintf("%d", i)
		if cm.ParentID != "" {
			parentIdx, ok := physIdx[cm.ParentID]
			if !ok {
				parentIdx = i // stale ParentID (e.g. parent no longer in range) -- best-effort fallback
			}
			indexStr = fmt.Sprintf("%d.%s", parentIdx, cm.SubRank)
		}

		view.Rows = append(view.Rows, statusRow(columns, map[string]string{
			"Icon":      icon,
			"Status":    word,
			"Index":     indexStr,
			"Stack Set": cm.StackSet,
			"git-sd-id": id,
			"Hash":      c.Hash[:7],
			"Message":   firstLine,
			"PR":        cm.PRURL,
		}))
	}
	return view, nil
}

// stdoutIsTerminal is ui.StdoutIsTerminal; tests replace it.
var stdoutIsTerminal = ui.StdoutIsTerminal

// statusWord is the text of a Status cell: the marker's word, colored when
// color is on.
func statusWord(marker ui.Marker, color bool) string { return statusWordWith(marker, "", color) }

// statusWordWith is statusWord with a progress suffix (" 7/12") after the word.
func statusWordWith(marker ui.Marker, suffix string, color bool) string {
	if color {
		return ui.Colorize(marker.Color, marker.Word+suffix)
	}
	return marker.Word + suffix
}

// ciProgress is the count shown after "CI running" (checks finished/total) and
// "CI failed" (checks failed/total); empty for every other marker, and when
// nothing is countable. Display only: the marker never depends on it.
func ciProgress(id ui.MarkerID, lookups map[int]prLookup, prNumber int) string {
	if id != ui.CIRunning && id != ui.CIFailed {
		return ""
	}
	lk, ok := lookups[prNumber]
	if !ok || lk.err != nil || lk.info == nil {
		return ""
	}
	finished, failed, total := lk.info.CIProgress()
	if total == 0 {
		return ""
	}
	if id == ui.CIFailed {
		return fmt.Sprintf(" %d/%d", failed, total)
	}
	return fmt.Sprintf(" %d/%d", finished, total)
}

// defaultStatusColumns is the default value of sd.status.columns.
const defaultStatusColumns = "Icon,Status,Index,Stack Set,git-sd-id,Hash,Message,PR"

// statusColumnNames are the columns status can show, in default order. Icon is
// the state's emoji and has a blank header; Status is the state's word.
var statusColumnNames = []string{"Icon", "Status", "Index", "Stack Set", "git-sd-id", "Hash", "Message", "PR"}

// normalizeColumnName makes column names compare loosely: case, spaces,
// hyphens and underscores don't matter ("stackset" is "Stack Set").
func normalizeColumnName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r != ' ' && r != '-' && r != '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// statusColumns parses sd.status.columns into canonical column names, in the
// order given. An unknown name, or no columns at all, is an error.
func statusColumns(configured string) ([]string, error) {
	var columns []string
	for part := range strings.SplitSeq(configured, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		canonical := ""
		for _, c := range statusColumnNames {
			if normalizeColumnName(c) == normalizeColumnName(name) {
				canonical = c
			}
		}
		if canonical == "" {
			return nil, fmt.Errorf("unknown status column '%s' (valid: %s)", name, strings.Join(statusColumnNames, ", "))
		}
		columns = append(columns, canonical)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("sd.status.columns lists no columns (valid: %s)", strings.Join(statusColumnNames, ", "))
	}
	return columns, nil
}

// statusHeader is the header row for columns: the Icon column's is blank.
func statusHeader(columns []string) []string {
	header := make([]string, len(columns))
	for i, c := range columns {
		if c != "Icon" {
			header[i] = c
		}
	}
	return header
}

// statusRow picks the cells for columns, in order; a column with no cell is blank.
func statusRow(columns []string, cells map[string]string) []string {
	row := make([]string, len(columns))
	for i, c := range columns {
		row[i] = cells[c]
	}
	return row
}

// subCommitMarker reports whether a sub-commit's row shows the Unsynced marker.
// A sub-commit has no PR of its own (it ships in its parent's), so it shows
// nothing of the PR's state; it only says when it isn't in that PR yet: the
// parent has a PR that is still live and the commit isn't recorded as pushed
// into it as it is now (new, amended, reworded, or moved into another PR).
func subCommitMarker(sub, parent metadata.CommitMetadata, parentMarker ui.MarkerID, fingerprint string) (ui.MarkerID, bool) {
	if parent.PRNumber == 0 || parentMarker == ui.Merged || parentMarker == ui.Closed {
		return "", false
	}
	if sub.SyncedFingerprint != "" && sub.SyncedFingerprint == fingerprint && sub.SyncedPR == parent.PRNumber {
		return "", false
	}
	return ui.Unsynced, true
}

// physIdxHas reports whether the commit is in the range being shown.
func physIdxHas(physIdx map[string]int, id string) bool {
	_, ok := physIdx[id]
	return ok
}

// markerID decides which marker a commit shows: Untracked without a git-sd-id,
// Ready if it is tracked but not assigned to a Stack Set, otherwise a marker for
// its PR (see prMarkerID).
func markerID(tracked bool, cm metadata.CommitMetadata, lookups map[int]prLookup) ui.MarkerID {
	switch {
	case !tracked:
		return ui.Untracked
	case cm.StackSet == "":
		return ui.Ready
	}
	return prMarkerID(lookups, cm.PRNumber)
}

// prMarkerID is the marker for a commit assigned to a Stack Set: Assigned if it
// has no PR yet, the PR's state if it could be read, and Unavailable if the
// lookup failed (or is missing) — never a guessed state.
func prMarkerID(lookups map[int]prLookup, prNumber int) ui.MarkerID {
	if prNumber == 0 {
		return ui.Assigned
	}
	lk, ok := lookups[prNumber]
	if !ok || lk.err != nil || lk.info == nil {
		return ui.Unavailable
	}
	return classifyPR(lk.info)
}

// classifyPR maps a PR's state to its marker. Merged and Closed always win;
// then a conflict GitHub reports, Changes Requested, failing CI, running CI, Draft (a draft can't be
// merged whatever its reviews say), and otherwise Approved or Review — an empty review decision (an
// unreviewed PR in a repo that doesn't require reviews) means nobody has
// reviewed it yet, so it is Review, never "Assigned" (which is only for a
// commit with no PR).
func classifyPR(info *github.DetailedPRInfo) ui.MarkerID {
	switch {
	case info.State == "MERGED":
		return ui.Merged
	case info.State == "CLOSED":
		return ui.Closed
	case info.HasConflict():
		return ui.Conflict
	case info.ReviewDecision == "CHANGES_REQUESTED":
		return ui.Changes
	case info.CIStatus() == github.CIFailing:
		return ui.CIFailed
	case info.CIStatus() == github.CIRunning:
		return ui.CIRunning
	case info.IsDraft:
		return ui.Draft
	case info.ReviewDecision == "APPROVED":
		return ui.Approved
	default:
		return ui.Review
	}
}

// legendRequested reports whether the legend should be shown: the configured
// default ("true" shows it), overridden by the last of --legend/--no-legend.
func legendRequested(args []string, configured string) bool {
	show := configured == "true"
	for _, arg := range args {
		switch arg {
		case "--legend":
			show = true
		case "--no-legend":
			show = false
		}
	}
	return show
}

// lookupFailures summarises the PR lookups that failed in one status run.
type lookupFailures struct {
	envErr error // gh missing/unauthenticated: every lookup fails the same way
	failed []int // PRs whose lookup failed for any other reason, in stack order
	first  error // the first of those errors
}

func summarizeLookups(numbers []int, results map[int]prLookup) lookupFailures {
	var f lookupFailures
	for _, n := range numbers {
		err := results[n].err
		switch {
		case err == nil:
		case github.IsEnvError(err):
			if f.envErr == nil {
				f.envErr = err
			}
		default:
			f.failed = append(f.failed, n)
			if f.first == nil {
				f.first = err
			}
		}
	}
	return f
}

// warnings returns the lines status prints (to stderr) after the table.
func (f lookupFailures) warnings() []string {
	var lines []string
	if f.envErr != nil {
		lines = append(lines, fmt.Sprintf("Warning: PR states unavailable: %v", f.envErr))
	}
	if len(f.failed) > 0 {
		lines = append(lines, fmt.Sprintf("Warning: could not read the state of PR %s: %v", formatPRList(f.failed), f.first))
	}
	return lines
}
