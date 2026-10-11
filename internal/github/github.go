package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PRInfo contains basic information about a PR.
type PRInfo struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	// BaseRefName and MergeCommit are only fetched by GetPRInfo (rebase uses
	// them to verify a MERGED PR actually landed on the base branch).
	BaseRefName string `json:"baseRefName,omitempty"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit,omitempty"`
}

// HasConflict reports whether GitHub says the PR conflicts with its base
// branch. UNKNOWN (still being computed) is not a conflict, and nothing is
// inferred locally: a base that differs from what the stack expects is not one.
func (d *DetailedPRInfo) HasConflict() bool {
	return d.Mergeable == "CONFLICTING" || d.MergeStateStatus == "DIRTY"
}

// CIStatus summarises a PR's checks: nothing to report, some still running, or
// at least one failed.
type CIStatus int

const (
	CINone CIStatus = iota
	CIRunning
	CIFailing
)

// CIStatus reads the PR's statusCheckRollup (check runs and commit statuses on
// its head commit). Failing wins over running. Cancelled, skipped, neutral and
// stale check runs count as neither, and a rollup that can't be read is no
// information rather than a guess.
func (d *DetailedPRInfo) CIStatus() CIStatus {
	if len(d.StatusCheckRollup) == 0 {
		return CINone
	}
	var entries []struct {
		Typename   string `json:"__typename"`
		Status     string `json:"status"`     // CheckRun: QUEUED, IN_PROGRESS, COMPLETED, ...
		Conclusion string `json:"conclusion"` // CheckRun, once completed
		State      string `json:"state"`      // StatusContext: SUCCESS, FAILURE, ERROR, PENDING, EXPECTED
	}
	if err := json.Unmarshal(d.StatusCheckRollup, &entries); err != nil {
		return CINone
	}
	running := false
	for _, e := range entries {
		switch e.Typename {
		case "CheckRun":
			switch e.Conclusion {
			case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
				return CIFailing
			}
			// WAITING is a job held at an approval gate (an environment review, a
			// wait timer, a concurrency queue); nothing is executing, so it is not CI running.
			if e.Status != "" && e.Status != "COMPLETED" && e.Status != "WAITING" {
				running = true
			}
		case "StatusContext":
			switch e.State {
			case "FAILURE", "ERROR":
				return CIFailing
			case "PENDING", "EXPECTED":
				running = true
			}
		}
	}
	if running {
		return CIRunning
	}
	return CINone
}

// DetailedPRInfo includes reviews and checks.
type DetailedPRInfo struct {
	PRInfo
	ReviewDecision    string          `json:"reviewDecision"`
	IsDraft           bool            `json:"isDraft"`
	StatusCheckRollup json.RawMessage `json:"statusCheckRollup"`
	// Mergeable is MERGEABLE, CONFLICTING or UNKNOWN (GitHub is still
	// working it out); MergeStateStatus is GitHub's finer-grained verdict
	// (CLEAN, DIRTY, BLOCKED, BEHIND, UNKNOWN, ...).
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
}

// CreatePR creates a new PR using gh.
func CreatePR(title, body, base, head string) (*PRInfo, error) {
	out, err := ghCombined("pr", "create", "--title", title, "--body", body, "--base", base, "--head", head)
	if err != nil {
		if IsEnvError(err) {
			return nil, err
		}
		output := string(out)
		if strings.Contains(output, "already exists:") {
			// Extract URL from output
			for line := range strings.SplitSeq(output, "\n") {
				if strings.HasPrefix(line, "https://github.com/") {
					return GetPRInfo(strings.TrimSpace(line))
				}
			}
		}
		return nil, fmt.Errorf("error creating PR: %w\nOutput: %s", err, output)
	}

	// `gh pr create` prints the new PR's URL; its number is the /pull/<n>
	// suffix and a new PR is open, so no second round trip is needed. Fall
	// back to a lookup if the output has no URL we recognise.
	if info, ok := newPRFromCreateOutput(string(out)); ok {
		return info, nil
	}
	return GetPRInfo(strings.TrimSpace(string(out)))
}

// prURLPattern matches a GitHub pull request URL.
var prURLPattern = regexp.MustCompile(`^https://[^/\s]+/[^/\s]+/[^/\s]+/pull/(\d+)$`)

// newPRFromCreateOutput extracts the freshly created PR from `gh pr create`'s
// output: the last line that is a pull request URL.
func newPRFromCreateOutput(out string) (*PRInfo, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		url := strings.TrimSpace(lines[i])
		if m := prURLPattern.FindStringSubmatch(url); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, false
			}
			return &PRInfo{Number: n, URL: url, State: "OPEN"}, true
		}
	}
	return nil, false
}

// UpdatePR updates an existing PR's title and body, and its base branch if base
// is not empty. GitHub rejects any base on a PR that is part of a registered
// Stack, even the one it already has, so callers pass "" unless it changes.
func UpdatePR(number int, title, body, base string) error {
	args := []string{"pr", "edit", fmt.Sprintf("%d", number), "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}
	_, err := ghOutput(args...)
	return err
}

// GetPRInfo retrieves PR info by URL or number.
func GetPRInfo(selector string) (*PRInfo, error) {
	out, err := ghOutput("pr", "view", selector, "--json", "number,url,state,baseRefName,mergeCommit")
	if err != nil {
		return nil, err
	}

	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetDetailedPRInfo includes reviews and checks.
func GetDetailedPRInfo(number int) (*DetailedPRInfo, error) {
	out, err := ghOutput("pr", "view", fmt.Sprintf("%d", number), "--json", "number,url,state,reviewDecision,statusCheckRollup,isDraft,mergeable,mergeStateStatus")
	if err != nil {
		return nil, err
	}

	var info DetailedPRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// BranchPush is one branch to force-push: the local commit and the remote
// branch name it becomes.
type BranchPush struct {
	Local  string
	Branch string
}

// PushResult is the outcome of pushing one branch: Pushed is false when the
// remote branch already had exactly that commit, and Err is set if this branch
// (or the whole push) failed.
type PushResult struct {
	Pushed bool
	Err    error
}

// PushBranches force-pushes every branch in one `git push` (one connection
// round trip instead of one per branch) and reports each branch's outcome, in
// the order given. A non-nil error return means nothing was attempted because
// gh is unusable (see EnvError); otherwise failures are per branch: a rejected
// ref fails only itself, while a failure of the whole push (no connection, bad
// credentials) fails every branch that git reported nothing for.
func PushBranches(remote string, pushes []BranchPush) ([]PushResult, error) {
	results := make([]PushResult, len(pushes))
	if len(pushes) == 0 {
		return results, nil
	}
	// Pushing is the one thing a command does before its first gh call, so
	// confirm once per process that gh can authenticate, before any push.
	if err := ensureReady(); err != nil {
		return nil, err
	}
	args := []string{"push", "--porcelain", "--force", remote}
	for _, p := range pushes {
		args = append(args, p.Local+":refs/heads/"+p.Branch)
	}
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	status := parsePushPorcelain(stdout.String())
	for i, p := range pushes {
		st, reported := status["refs/heads/"+p.Branch]
		switch {
		case reported && st.flag == '!':
			results[i].Err = fmt.Errorf("git push failed: %s\nOutput: %s", st.summary, pushHint(stderr.String()))
		case reported:
			results[i].Pushed = st.flag != '='
		case runErr != nil:
			results[i].Err = fmt.Errorf("git push failed: %w\nOutput: %s", runErr, pushHint(stderr.String()+stdout.String()))
		default:
			results[i].Pushed = true // pushed, but git reported nothing for it: assume it changed
		}
	}
	return results, nil
}

// pushStatus is one `git push --porcelain` ref line: flag is ' ' (fast-forward),
// '+' (forced), '*' (new), '-' (deleted), '=' (up to date) or '!' (rejected).
type pushStatus struct {
	flag    byte
	summary string
}

// parsePushPorcelain reads `git push --porcelain` output, one
// "<flag>\t<from>:<to>\t<summary>" line per ref, keyed by the destination ref.
func parsePushPorcelain(out string) map[string]pushStatus {
	status := map[string]pushStatus{}
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 2 || line[1] != '\t' || !strings.ContainsRune(" +-*=!", rune(line[0])) {
			continue
		}
		refspec, summary, _ := strings.Cut(line[2:], "\t")
		_, to, ok := strings.Cut(refspec, ":")
		if !ok {
			continue
		}
		status[to] = pushStatus{flag: line[0], summary: strings.TrimSpace(summary)}
	}
	return status
}

// PushBranch force-pushes one branch; see PushBranches.
func PushBranch(remote, local, remoteBranch string) (pushed bool, err error) {
	results, err := PushBranches(remote, []BranchPush{{Local: local, Branch: remoteBranch}})
	if err != nil {
		return false, err
	}
	return results[0].Pushed, results[0].Err
}

// OpenPRsBasedOn returns the numbers of open PRs whose base branch is
// branch. sync checks this before deleting a finished branch, since
// deleting a PR's base branch makes GitHub close that PR.
func OpenPRsBasedOn(branch string) ([]int, error) {
	out, err := ghOutput("pr", "list", "--base", branch, "--state", "open", "--json", "number")
	if err != nil {
		if IsEnvError(err) {
			return nil, err
		}
		return nil, fmt.Errorf("gh pr list --base %s failed: %w", branch, err)
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parsing gh pr list output: %w", err)
	}
	var numbers []int
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	return numbers, nil
}

// DeleteRemoteBranch deletes a branch from the given remote. A branch that
// is already gone (e.g. GitHub auto-deleted it on merge) is not an error.
func DeleteRemoteBranch(remote, branch string) error {
	if err := ensureReady(); err != nil {
		return err
	}
	out, err := exec.Command("git", "push", remote, "--delete", branch).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "remote ref does not exist") {
		return fmt.Errorf("git push --delete failed: %w\nOutput: %s", err, pushHint(string(out)))
	}
	return nil
}

// ErrAsyncMergeUnavailable indicates the repo or token lacks access to
// GitHub's Stacked Pull Requests REST API (a 404 from any Stacks or
// async-merge endpoint). Surfaced distinctly so it reads as "this backend
// isn't available here" rather than a confusing generic failure.
var ErrAsyncMergeUnavailable = errors.New("GitHub's Stacked Pull Requests API is not available for this repository")

// StackInfo is a registered GitHub Stack, as returned by the create/get/
// extend/unstack Stacks endpoints.
type StackInfo struct {
	Number       int `json:"number"`
	PullRequests []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	} `json:"pull_requests"`
}

// CreateStack registers an ordered list of pull requests (bottom of the
// stack to top) as a new GitHub Stack. Each PR's base ref must already match
// the previous PR's head ref, forming a valid chain — sync's normal PR
// base-chaining already produces this before CreateStack is called.
func CreateStack(prNumbers []int) (*StackInfo, error) {
	body, err := json.Marshal(map[string][]int{"pull_requests": prNumbers})
	if err != nil {
		return nil, err
	}
	out, err := runStacksAPI("POST", "repos/{owner}/{repo}/stacks", body)
	if err != nil {
		return nil, err
	}
	var info StackInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("error parsing create-stack response: %w\n%s", err, out)
	}
	return &info, nil
}

// GetStack returns the PR numbers of a registered Stack, bottom to top
// (including any that are already merged). Any error, including the Stack no
// longer existing, means the caller can't rely on the registration.
func GetStack(stackNumber int) ([]int, error) {
	out, err := runStacksAPI("GET", fmt.Sprintf("repos/{owner}/{repo}/stacks/%d", stackNumber), nil)
	if err != nil {
		return nil, err
	}
	var info StackInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("error parsing stack response: %w\n%s", err, out)
	}
	numbers := make([]int, 0, len(info.PullRequests))
	for _, pr := range info.PullRequests {
		numbers = append(numbers, pr.Number)
	}
	return numbers, nil
}

// Unstack dissolves a stack's registration. PRs that are merged, merging, or
// queued for merge can't be unstacked and are left in place rather than
// erroring — callers that need a fully clean slate before re-registering
// should account for that rather than assume Unstack always clears
// everything.
func Unstack(stackNumber int) error {
	_, err := runStacksAPI("POST", fmt.Sprintf("repos/{owner}/{repo}/stacks/%d/unstack", stackNumber), nil)
	if err != nil && !IsEnvError(err) && strings.Contains(err.Error(), "cannot be removed from this stack") {
		// Every PR in the Stack is merged (or merging): nothing is left to
		// unstack, which is the outcome the caller wants.
		return nil
	}
	return err
}

func runStacksAPI(method, path string, body []byte) ([]byte, error) {
	args := []string{"api", "--method", method}
	if body != nil {
		args = append(args, "--input", "-")
	}
	args = append(args, path)
	out, err := ghCombinedStdin(body, args...)
	if err != nil {
		if IsEnvError(err) {
			return nil, err
		}
		if strings.Contains(string(out), "HTTP 404") {
			return nil, ErrAsyncMergeUnavailable
		}
		return nil, fmt.Errorf("gh api %s %s failed: %w\n%s", method, path, err, string(out))
	}
	return out, nil
}

// mergeAsyncResult is the response body shape shared by the submit and poll
// async-merge endpoints.
type mergeAsyncResult struct {
	Status  string `json:"status"` // "pending", "merged", "enqueued", or "failed"
	Details struct {
		UUID    string `json:"uuid"`
		Message string `json:"message"`
		SHA     string `json:"sha"`
	} `json:"details"`
}

// submitStackMerge submits pull request `number` to GitHub's async
// stack-merge API. Because this merges every PR in the stack up to and
// including `number`, submitting the target at the top of a --upto range is
// sufficient to cascade-merge everything below it. GitHub returns a JSON
// body describing the result even on non-2xx responses (e.g. 409 "already in
// flight"), so the body is parsed first and only treated as a hard failure
// if it doesn't parse.
func submitStackMerge(number int) (*mergeAsyncResult, error) {
	// merge_method: "squash" matches the pre-migration behavior of always
	// calling `gh pr merge --squash` — a PR's own sub-commits (grouped onto
	// one branch by GroupCommits) still collapse into a single commit on the
	// base branch, even though the cross-PR consolidation squash is gone.
	path := fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/merge-async", number)
	out, err := ghCombined("api", "--method", "PUT", path, "-f", "merge_method=squash")
	if IsEnvError(err) {
		return nil, err
	}

	var result mergeAsyncResult
	if jsonErr := json.Unmarshal(out, &result); jsonErr == nil && result.Status != "" {
		return &result, nil
	}
	if err != nil {
		if strings.Contains(string(out), "HTTP 404") {
			return nil, ErrAsyncMergeUnavailable
		}
		return nil, fmt.Errorf("error submitting async merge for PR #%d: %w\n%s", number, err, string(out))
	}
	return nil, fmt.Errorf("error parsing async merge response for PR #%d\n%s", number, out)
}

// pollStackMerge polls a pending async merge to a terminal state, backing off
// from 500ms up to a 2s ceiling between attempts (rather than a flat 2s) so
// the common case of a merge completing quickly returns sooner, while the
// overall wait is still capped at maxWait (5 minutes) — GitHub documents a
// stack merge can take "a few minutes", and a 2 minute cap made merge give up on
// merges GitHub was still processing.
func pollStackMerge(number int, uuid string) (*mergeAsyncResult, error) {
	path := fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/merge-async/%s", number, uuid)
	const maxWait = 5 * time.Minute
	const minInterval = 500 * time.Millisecond
	const maxInterval = 2 * time.Second

	deadline := time.Now().Add(maxWait)
	interval := minInterval
	for {
		out, err := ghCombined("api", path)
		if err != nil {
			if IsEnvError(err) {
				return nil, err
			}
			return nil, fmt.Errorf("error polling async merge for PR #%d: %w\n%s", number, err, string(out))
		}
		var result mergeAsyncResult
		if err := json.Unmarshal(out, &result); err != nil {
			return nil, fmt.Errorf("error parsing async merge poll response for PR #%d: %w\n%s", number, err, out)
		}
		if result.Status != "pending" {
			return &result, nil
		}
		if !time.Now().Add(interval).Before(deadline) {
			return nil, fmt.Errorf("PR #%d's async merge is still pending after waiting %s; check its status manually", number, maxWait)
		}
		time.Sleep(interval)
		interval = min(interval*3/2, maxInterval)
	}
}

// MergeStack submits pull request `number` for GitHub's native async stack
// merge and polls it to a terminal state. This merges every PR in the
// bottom-through-`number` range individually — each lands on the base branch
// as its own commit and every PR above it is server-side rebased onto the
// new base — rather than git-sd squashing them into one commit itself.
// "enqueued" (added to a merge queue rather than merged directly) is treated
// as an error here: git-sd's caller drops merged commits from the local
// branch immediately after, which would be wrong for a merge that hasn't
// landed yet.
func MergeStack(number int) error {
	result, err := submitStackMerge(number)
	if err != nil {
		return err
	}
	if result.Status == "pending" {
		result, err = pollStackMerge(number, result.Details.UUID)
		if err != nil {
			return err
		}
	}

	switch result.Status {
	case "merged":
		return nil
	case "enqueued":
		return fmt.Errorf("PR #%d was added to a merge queue rather than merged directly; re-run 'git sd merge' once the queue processes it", number)
	case "failed":
		return fmt.Errorf("async merge of PR #%d failed: %s", number, result.Details.Message)
	default:
		return fmt.Errorf("async merge of PR #%d returned unexpected status %q", number, result.Status)
	}
}
