package git

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// GenerateID creates a 5-character alphanumeric hash.
func GenerateID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, 5)
	rand.Read(result)
	for i := range 5 {
		result[i] = chars[int(result[i])%len(chars)]
	}
	return "s" + string(result[1:]) // Ensure it starts with 's' for 'sd'
}

// GetCommitMessage returns the message of a commit.
func GetCommitMessage(hash string) (string, error) {
	cmd := exec.Command("git", "log", "-1", "--format=%B", hash)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Commit represents a git commit.
type Commit struct {
	Hash    string
	Message string
}

// PatchIDs returns the stable patch-id of every non-merge commit in
// since..HEAD, keyed by commit hash, from ONE `git log -p | git patch-id
// --stable` pipeline. A patch-id depends on a commit's diff, not its hash or
// message, so it survives a rebase that leaves the diff alone. A commit with an
// empty diff has no entry.
func PatchIDs(since string) (map[string]string, error) {
	logCmd := exec.Command("git", "log", "-p", "--no-merges", "--no-color", "--no-ext-diff", since+"..HEAD")
	logOut, err := logCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log -p failed: %w", err)
	}
	idCmd := exec.Command("git", "patch-id", "--stable")
	idCmd.Stdin = bytes.NewReader(logOut)
	idOut, err := idCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git patch-id failed: %w", err)
	}
	ids := map[string]string{}
	for line := range strings.SplitSeq(string(idOut), "\n") {
		patchID, hash, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && patchID != "" && hash != "" {
			ids[hash] = patchID
		}
	}
	return ids, nil
}

// GetMergeBase finds the merge base between the current branch and the target.
func GetMergeBase(target string) (string, error) {
	cmd := exec.Command("git", "merge-base", "HEAD", target)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ParseBaseBranch splits value into its remote and branch parts. value must
// be of the form "<remote>/<branch>"; both parts must be non-empty. This is
// pure string logic — it does not check whether remote is actually
// configured (see resolveRemote), so it stays hermetically unit-testable
// regardless of the ambient repo's real remotes.
func ParseBaseBranch(value string) (remote, branch string, err error) {
	remote, branch, ok := strings.Cut(value, "/")
	if !ok || remote == "" || branch == "" {
		return "", "", fmt.Errorf(
			"sd.base-branch %q must be of the form \"<remote>/<branch>\" (e.g. %q)", value, "origin/main")
	}
	return remote, branch, nil
}

// ListRemotes returns the names of the repository's configured git remotes.
func ListRemotes() ([]string, error) {
	cmd := exec.Command("git", "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// resolveRemote parses baseBranch and checks that its remote is one of the
// given, already-fetched configured remotes.
func resolveRemote(baseBranch string, remotes []string) (string, error) {
	remote, _, err := ParseBaseBranch(baseBranch)
	if err != nil {
		return "", err
	}
	if !slices.Contains(remotes, remote) {
		return "", fmt.Errorf(
			"sd.base-branch %q targets remote %q, but no such remote is configured (configured remotes: %s)",
			baseBranch, remote, strings.Join(remotes, ", "))
	}
	return remote, nil
}

// configuredBaseBranch returns sd.base-branch's value if set. If unset, it
// picks a deterministic default: prefer a remote literally named "origin"
// (this tool's historical convention), else the sole configured remote if
// there is exactly one, else fail — with no "origin" and more than one (or
// zero) remotes, there's no non-arbitrary choice, so the user must set
// sd.base-branch explicitly.
func configuredBaseBranch(remotes []string) (string, error) {
	if val, err := ConfigGet("sd.base-branch"); err == nil && val != "" {
		return val, nil
	}
	switch {
	case slices.Contains(remotes, "origin"):
		return "origin/main", nil
	case len(remotes) == 1:
		return remotes[0] + "/main", nil
	default:
		return "", fmt.Errorf(
			"sd.base-branch is not set and no remote could be inferred (configured remotes: %s); set sd.base-branch to \"<remote>/<branch>\"",
			strings.Join(remotes, ", "))
	}
}

// ResolveBaseBranch determines sd.base-branch (explicit or inferred, see
// configuredBaseBranch), validates it — including that its remote is
// actually configured — and resolves the merge base against HEAD. Commands
// that depend on the base branch must call this first, before any other work.
func ResolveBaseBranch() (baseBranch, remote, since string, err error) {
	remotes, err := ListRemotes()
	if err != nil {
		return "", "", "", fmt.Errorf("could not list git remotes: %w", err)
	}
	baseBranch, err = configuredBaseBranch(remotes)
	if err != nil {
		return "", "", "", err
	}
	remote, err = resolveRemote(baseBranch, remotes)
	if err != nil {
		return "", "", "", err
	}
	since, err = GetMergeBase(baseBranch)
	if err != nil {
		return "", "", "", fmt.Errorf(
			"could not resolve merge base against %q: %w\nHint: if %q is a remote-tracking branch you haven't fetched yet, try 'git fetch %s' and retry",
			baseBranch, err, baseBranch, remote)
	}
	return baseBranch, remote, since, nil
}

// BaseBranchRemote returns just sd.base-branch's configured (or inferred)
// remote name, validated — for callers (cleanup) that need to know where
// git-sd's branches live but don't need a merge-base.
func BaseBranchRemote() (string, error) {
	remotes, err := ListRemotes()
	if err != nil {
		return "", fmt.Errorf("could not list git remotes: %w", err)
	}
	baseBranch, err := configuredBaseBranch(remotes)
	if err != nil {
		return "", err
	}
	return resolveRemote(baseBranch, remotes)
}

// GetCommitsInRange returns commits from (since) to HEAD.
func GetCommitsInRange(since string) ([]Commit, error) {
	cmd := exec.Command("git", "log", "--format=%H%x00%B%x00", since+"..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	parts := strings.Split(string(out), "\x00")
	var commits []Commit
	for i := 0; i+1 < len(parts); i += 2 {
		hash := strings.TrimSpace(parts[i])
		if hash == "" {
			continue
		}
		commits = append(commits, Commit{
			Hash:    hash,
			Message: parts[i+1],
		})
	}

	// Git log returns commits in reverse chronological order (newest first).
	// We want them in chronological order (oldest first).
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	return commits, nil
}

// RewriteCommitsWithIDs ensures all commits from 'since' have a git-sd-id.
func RewriteCommitsWithIDs(since string) error {
	commits, err := GetCommitsInRange(since)
	if err != nil {
		return err
	}

	parent := since
	changed := false

	for _, c := range commits {
		id := ExtractTrailer(c.Message, "git-sd-id")
		if id == "" {
			id = GenerateID()
			newMessage, err := AddTrailer(c.Message, id)
			if err != nil {
				return err
			}

			// Create new commit object
			treeHash, err := GetTreeHash(c.Hash)
			if err != nil {
				return err
			}

			newHash, err := CreateCommit(treeHash, parent, newMessage)
			if err != nil {
				return err
			}
			parent = newHash
			changed = true
		} else {
			if !changed {
				parent = c.Hash
			} else {
				// We must rebuild even if it has an ID because its parent changed
				treeHash, err := GetTreeHash(c.Hash)
				if err != nil {
					return err
				}
				newHash, err := CreateCommit(treeHash, parent, c.Message)
				if err != nil {
					return err
				}
				parent = newHash
			}
		}
	}

	if changed {
		// Update current branch to point to the new head
		return updateRef("HEAD", parent)
	}

	return nil
}

// GetTreeHash returns the tree hash of a commit.
func GetTreeHash(commitHash string) (string, error) {
	cmd := exec.Command("git", "log", "-1", "--format=%T", commitHash)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateCommit creates a new commit object.
func CreateCommit(treeHash, parentHash, message string) (string, error) {
	cmd := exec.Command("git", "commit-tree", treeHash, "-p", parentHash, "-m", message)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RebuildCommitChainRebase rebuilds the branch to match the given commit
// order with a real 'git rebase -i' onto since, so a conflict leaves an
// ordinary rebase in progress: 'git rebase --continue' replays the remaining
// commits and 'git rebase --abort' restores the branch exactly as it was.
// (A reset+cherry-pick loop can do neither.) On failure,
// RebaseInProgress distinguishes a conflict from a rebase that never started
// (e.g. a dirty working tree).
func RebuildCommitChainRebase(since string, commits []Commit) error {
	return RebasePicks(since, since, "keep", commits)
}

// RebasePicks runs 'git rebase -i --onto <onto> <upstream>' with a todo of
// exactly the given picks, so the branch becomes onto + those commits in that
// order. empty is git's --empty mode: "keep" retains a pick that turns out
// empty, "drop" drops one whose change is already upstream (supported by
// every git >= 2.26, unlike "stop"). With no commits the branch is simply
// reset to onto (rebase -i with an empty todo would do nothing).
func RebasePicks(onto, upstream, empty string, commits []Commit) error {
	if len(commits) == 0 {
		return ResetHard(onto)
	}
	todo, err := os.CreateTemp("", "git-sd-todo-*")
	if err != nil {
		return err
	}
	defer os.Remove(todo.Name())
	for _, c := range commits {
		fmt.Fprintf(todo, "pick %s\n", c.Hash)
	}
	if err := todo.Close(); err != nil {
		return err
	}

	args := []string{"rebase", "-i", "--no-autosquash", "--no-autostash", "--empty=" + empty}
	if onto != upstream {
		args = append(args, "--onto", onto)
	}
	args = append(args, upstream)
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_SEQUENCE_EDITOR=cp '"+todo.Name()+"'")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rebuilding commit chain: %w\n%s", err, string(out))
	}
	return nil
}

// RebaseStoppedSHA returns the full hash of the commit an in-progress rebase
// is stopped on (the original commit being replayed), or "" if unknown.
func RebaseStoppedSHA() string {
	out, err := exec.Command("git", "rev-parse", "--git-path", "rebase-merge/stopped-sha").Output()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	full, err := RevParse(strings.TrimSpace(string(data)))
	if err != nil {
		return ""
	}
	return full
}

// ConflictedFiles lists the paths with unresolved conflicts in the index.
func ConflictedFiles() []string {
	out, err := exec.Command("git", "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// RebaseInProgress reports whether a rebase is paused in this worktree.
func RebaseInProgress() bool {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		out, err := exec.Command("git", "rev-parse", "--git-path", name).Output()
		if err != nil {
			continue
		}
		if _, err := os.Stat(strings.TrimSpace(string(out))); err == nil {
			return true
		}
	}
	return false
}

// CurrentRef returns the current branch name if HEAD is on a branch, or the
// commit hash if HEAD is detached.
func CurrentRef() (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SquashCommitsOntoBase builds a single new commit combining exactly the
// given commits (in order) applied on top of `since`, with the given message.
// It uses a real cherry-pick sequence rather than tree reuse, so the result
// contains only these commits' own changes — even if other, unrelated commits
// sit between `since` and them in the branch's physical history (e.g. a
// different, still-open Stack Set interleaved on the same branch). The
// caller's original checkout is restored before returning, on both success
// and failure.
func SquashCommitsOntoBase(since string, hashes []string, message string) (string, error) {
	originalRef, err := CurrentRef()
	if err != nil {
		return "", fmt.Errorf("failed to determine current ref: %w", err)
	}

	if out, err := exec.Command("git", "checkout", "--detach", since).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to checkout base for squash: %w\n%s", err, string(out))
	}

	for _, h := range hashes {
		if out, err := exec.Command("git", "cherry-pick", h).CombinedOutput(); err != nil {
			exec.Command("git", "cherry-pick", "--abort").Run()
			exec.Command("git", "checkout", originalRef).Run()
			return "", fmt.Errorf("conflict cherry-picking %s while building squash commit: %w\n%s", h, err, string(out))
		}
	}

	if out, err := exec.Command("git", "reset", "--soft", since).CombinedOutput(); err != nil {
		exec.Command("git", "checkout", originalRef).Run()
		return "", fmt.Errorf("failed to soft-reset squash chain: %w\n%s", err, string(out))
	}

	if out, err := exec.Command("git", "commit", "-m", message).CombinedOutput(); err != nil {
		exec.Command("git", "checkout", originalRef).Run()
		return "", fmt.Errorf("failed to create squash commit: %w\n%s", err, string(out))
	}

	newHashOut, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		exec.Command("git", "checkout", originalRef).Run()
		return "", fmt.Errorf("failed to resolve new squash commit hash: %w", err)
	}
	newHash := strings.TrimSpace(string(newHashOut))

	if out, err := exec.Command("git", "checkout", originalRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("squash succeeded (commit %s) but failed to restore original checkout %s: %w\n%s", newHash, originalRef, err, string(out))
	}

	return newHash, nil
}

// FetchRemote updates remote-tracking refs for the given remote.
func FetchRemote(remote string) error {
	if out, err := exec.Command("git", "fetch", remote).CombinedOutput(); err != nil {
		text := string(out)
		if hint := AuthHint(text); hint != "" {
			text += "\n" + hint
		}
		return fmt.Errorf("git fetch %s failed: %w\n%s", remote, err, text)
	}
	return nil
}

// authFailureMarkers are fragments of git's output when a network operation
// could not authenticate (including a credential helper that isn't there).
var authFailureMarkers = []string{
	"could not read Username",
	"could not read Password",
	"Authentication failed",
	"terminal prompts disabled",
	"gh: command not found",
	"gh: not found",
}

// AuthHint returns a one-line suggestion if output from a failed git network
// command looks like an authentication failure, and "" otherwise. It is only
// ever appended to git's own error text, never a replacement for it.
func AuthHint(output string) string {
	for _, marker := range authFailureMarkers {
		if strings.Contains(output, marker) {
			return "If you use gh as git's credential helper, run 'gh auth login' and 'gh auth setup-git'."
		}
	}
	return ""
}

// ConfigGet retrieves a git configuration value.
func ConfigGet(key string) (string, error) {
	cmd := exec.Command("git", "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ConfigGetWithDefault retrieves a git configuration value or returns a default.
func ConfigGetWithDefault(key, defaultValue string) string {
	val, err := ConfigGet(key)
	if err != nil {
		return defaultValue
	}
	return val
}

// BranchNameSlug builds a branch name from a commit subject line plus a
// git-sd-id suffix: lowercase, runs of non-[a-z0-9] characters collapsed to a
// single "-", trimmed, and truncated to roughly 45 characters at a "-"
// boundary. If the subject slugifies to empty (e.g. pure emoji or non-Latin
// script), the git-sd-id alone is used as the branch name.
//
// The branch identity is the top-level commit's git-sd-id, NOT the physical
// index or the subject text: git-sd-id is stable across amend/rebase/
// reorder/partial-merge, so the suffix keeps the branch collision-safe and
// traceable even though the slug prefix is purely cosmetic. Callers compute
// this once, at first sync, and persist it in metadata — never recomputing
// it on a later message edit, so rewording a commit doesn't rename its
// branch or churn its PR.
func BranchNameSlug(subject, gitSDID string) string {
	const maxSlugLen = 45

	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(subject) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")

	if len(slug) > maxSlugLen {
		slug = slug[:maxSlugLen]
		if i := strings.LastIndexByte(slug, '-'); i > 0 {
			slug = slug[:i]
		}
	}

	if slug == "" {
		return gitSDID
	}
	return slug + "-" + gitSDID
}

// ExtractTrailer gets the value of a trailer from a message.
func ExtractTrailer(message, key string) string {
	cmd := exec.Command("git", "interpret-trailers", "--parse")
	cmd.Stdin = strings.NewReader(message)
	out, _ := cmd.Output()

	for line := range strings.SplitSeq(string(out), "\n") {
		if after, ok := strings.CutPrefix(line, key+": "); ok {
			return after
		}
	}
	return ""
}

// AddTrailer adds or updates a git-sd-id trailer in a commit message.
func AddTrailer(message, id string) (string, error) {
	cmd := exec.Command("git", "interpret-trailers", "--where", "end", "--if-exists", "replace", "--trailer", "git-sd-id="+id)
	cmd.Stdin = strings.NewReader(message)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// StripTrailer removes a "key: value" trailer line from the end of a commit
// message, along with the blank line that separated it from the body. It
// assumes the trailer is the message's final line, which holds for any
// trailer AddTrailer added (via "--where end").
func StripTrailer(message, key string) string {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], key+": ") {
		return message
	}
	lines = lines[:len(lines)-1]
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// BuildTrailerStrippedChain cherry-picks the given commits (in order) onto
// `since`, re-creating each one without its git-sd-id trailer before the
// branch is pushed to GitHub — git-sd-id is purely internal bookkeeping
// (metadata.json remains its source of truth) and never belongs in a PR a
// reviewer sees. Returns the new chain's tip hash. Mirrors
// SquashCommitsOntoBase's checkout/restore skeleton, but keeps every commit
// distinct rather than squashing them.
//
// The result is deterministic: each pushed commit is written with
// commit-tree using the original commit's author and committer identity,
// and its author date as the committer date. Author dates survive local
// rewrites (move/rebase/fixup cherry-picks, amend) while committer dates
// don't, so an unchanged group on an unchanged base is rebuilt bit-for-bit
// and sync's force-push of it is a no-op on GitHub. commit-tree also runs no
// hooks, so a commit-msg hook can't inject per-run differences.
func BuildTrailerStrippedChain(since string, commits []Commit) (string, error) {
	originalRef, err := CurrentRef()
	if err != nil {
		return "", fmt.Errorf("failed to determine current ref: %w", err)
	}

	if out, err := exec.Command("git", "checkout", "--detach", since).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to checkout base for trailer strip: %w\n%s", err, string(out))
	}
	fail := func(format string, args ...any) (string, error) {
		exec.Command("git", "checkout", originalRef).Run()
		return "", fmt.Errorf(format, args...)
	}

	parent, err := RevParse("HEAD")
	if err != nil {
		return fail("failed to resolve base %s: %w", since, err)
	}
	for _, c := range commits {
		if out, err := exec.Command("git", "cherry-pick", c.Hash).CombinedOutput(); err != nil {
			exec.Command("git", "cherry-pick", "--abort").Run()
			return fail("conflict cherry-picking %s while building push chain: %w\n%s", c.Hash, err, string(out))
		}
		tree, err := RevParse("HEAD^{tree}")
		if err != nil {
			return fail("failed to resolve tree for %s: %w", c.Hash, err)
		}
		env, err := pushedCommitEnv(c.Hash)
		if err != nil {
			return fail("failed to read identity of %s: %w", c.Hash, err)
		}
		message, err := stripspace(StripTrailer(c.Message, "git-sd-id"))
		if err != nil {
			return fail("failed to clean message of %s: %w", c.Hash, err)
		}
		cmd := exec.Command("git", "commit-tree", tree, "-p", parent, "-F", "-")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(message)
		out, err := cmd.Output()
		if err != nil {
			return fail("failed to write trailer-stripped copy of %s: %w", c.Hash, err)
		}
		parent = strings.TrimSpace(string(out))
		if out, err := exec.Command("git", "reset", "-q", "--soft", parent).CombinedOutput(); err != nil {
			return fail("failed to advance push chain to %s: %w\n%s", parent, err, string(out))
		}
	}

	if out, err := exec.Command("git", "checkout", originalRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("push chain built (tip %s) but failed to restore original checkout %s: %w\n%s", parent, originalRef, err, string(out))
	}

	return parent, nil
}

// pushedCommitEnv returns the environment for writing hash's pushed copy:
// its own author and committer identity, with its author date as both
// dates (see BuildTrailerStrippedChain).
func pushedCommitEnv(hash string) ([]string, error) {
	out, err := exec.Command("git", "log", "-1", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce", hash).Output()
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(f) != 5 {
		return nil, fmt.Errorf("unexpected identity format for %s: %q", hash, out)
	}
	return append(os.Environ(),
		"GIT_AUTHOR_NAME="+f[0], "GIT_AUTHOR_EMAIL="+f[1], "GIT_AUTHOR_DATE="+f[2],
		"GIT_COMMITTER_NAME="+f[3], "GIT_COMMITTER_EMAIL="+f[4], "GIT_COMMITTER_DATE="+f[2],
	), nil
}

// stripspace applies the whitespace cleanup `git commit -m` performs, so
// pushed messages look exactly as they did when built with commit --amend.
func stripspace(message string) (string, error) {
	cmd := exec.Command("git", "stripspace")
	cmd.Stdin = strings.NewReader(message)
	out, err := cmd.Output()
	return string(out), err
}

// SimulateChainBuild reports whether cherry-picking commits (in order) onto
// base would apply cleanly, using plumbing only — no checkout, no
// working-tree/index mutation, safe even with uncommitted local changes.
// Mirrors what BuildTrailerStrippedChain will actually do at sync time,
// without touching the repository: each commit is merged against the
// running tree with its own parent forced as the merge-base, so the merge
// exactly models a cherry-pick of that commit's own diff; on success the
// resulting tree feeds into the next commit's merge as the new base, so a
// whole chain can be replayed without ever checking anything out.
// Returns the index of the first commit that fails to apply, or -1 if the
// whole chain applies cleanly.
//
// Uses 'git merge-tree --write-tree --merge-base=' (git >= 2.38) when
// available, since it's backed by the modern merge-ort strategy (real
// content-based rename detection, matching what an actual 'git cherry-pick'
// would do). On older git, falls back to 'git read-tree -m' plumbing into a
// scratch index — see simulateOneCommitReadTree's doc comment for the
// resulting fidelity gap.
func SimulateChainBuild(base string, commits []Commit) (failedAt int, err error) {
	_, failedAt, err = SimulateChainTree(base, commits)
	return failedAt, err
}

// SimulateChainTree is SimulateChainBuild, additionally returning the tree
// hash the whole chain produces when it applies cleanly (failedAt == -1) —
// used by rebase to check whether a group's commits replayed onto an
// upstream commit reproduce a merge commit's tree exactly.
func SimulateChainTree(base string, commits []Commit) (tree string, failedAt int, err error) {
	simulateOne := simulateOneCommitReadTree
	if major, minor, patch, verr := gitVersion(); verr == nil && atLeast(major, minor, patch, 2, 38, 0) {
		simulateOne = simulateOneCommitMergeTree
	}

	tree = base
	for i, c := range commits {
		resultTree, conflict, err := simulateOne(tree, c)
		if err != nil {
			return "", -1, err
		}
		if conflict {
			return "", i, nil
		}
		tree = resultTree
	}
	tree, err = RevParse(tree + "^{tree}")
	if err != nil {
		return "", -1, err
	}
	return tree, -1, nil
}

// RevParse resolves rev to a full object hash.
func RevParse(rev string) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", rev).Output()
	if err != nil {
		return "", fmt.Errorf("could not resolve %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether commit a is an ancestor of (or equal to) b.
func IsAncestor(a, b string) bool {
	return exec.Command("git", "merge-base", "--is-ancestor", a, b).Run() == nil
}

// IsClean reports whether the working tree and index have no uncommitted
// changes to tracked files (untracked files are ignored, as with rebase).
func IsClean() (bool, error) {
	out, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

// ResetHard moves the current branch (and working tree) to rev.
func ResetHard(rev string) error {
	if out, err := exec.Command("git", "reset", "--hard", rev).CombinedOutput(); err != nil {
		return fmt.Errorf("git reset --hard %s failed: %w\n%s", rev, err, string(out))
	}
	return nil
}

// UpdateRef points ref at hash.
func UpdateRef(ref, hash string) error {
	if out, err := exec.Command("git", "update-ref", ref, hash).CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref %s failed: %w\n%s", ref, err, string(out))
	}
	return nil
}

// DeleteRef deletes ref (best-effort; a missing ref is not an error).
func DeleteRef(ref string) {
	exec.Command("git", "update-ref", "-d", ref).Run()
}

// simulateOneCommitMergeTree simulates cherry-picking c onto tree using
// 'git merge-tree --write-tree --merge-base=<c.Hash>^' (git >= 2.38): a real
// three-way merge (merge-ort) that writes the resulting tree without
// touching the index or working tree. Returns the resulting tree on a clean
// apply, conflict=true on a real merge conflict, or a non-nil err for any
// other command failure (bad ref, corrupt object, etc.).
func simulateOneCommitMergeTree(tree string, c Commit) (resultTree string, conflict bool, err error) {
	cmd := exec.Command("git", "merge-tree", "--write-tree", "--merge-base="+c.Hash+"^", tree, c.Hash)
	out, cmdErr := cmd.Output()
	if cmdErr == nil {
		return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), false, nil
	}
	exitErr, ok := cmdErr.(*exec.ExitError)
	if ok && exitErr.ExitCode() == 1 {
		return "", true, nil
	}
	stderr := ""
	if ok {
		stderr = string(exitErr.Stderr)
	}
	return "", false, fmt.Errorf("git merge-tree failed unexpectedly at %s: %w\n%s", c.Hash, cmdErr, stderr)
}

// simulateOneCommitReadTree is the pre-2.38 fallback for
// simulateOneCommitMergeTree: it performs the same three-way merge (ancestor
// c.Hash^, ours tree, theirs c.Hash) via 'git read-tree -m --aggressive' into
// a scratch index (never the repository's real index), then 'git write-tree'
// to materialize the result.
//
// read-tree -m always exits 0 even when the merge conflicts — it leaves
// multi-stage (unmerged) entries in the index instead of failing — so
// conflicts are detected afterward via 'git ls-files -u' rather than the
// command's exit code; a nonzero exit from read-tree or write-tree is a
// genuine command failure, not a conflict.
//
// Known fidelity gap: read-tree's merge doesn't do the content-based rename
// detection that merge-tree's merge-ort backend does, so a path renamed on
// one side and edited on the other can report a conflict (or resolve
// differently) here where the modern path would apply cleanly. There is no
// read-tree flag that closes this gap; it's accepted as the cost of
// supporting git < 2.38.
func simulateOneCommitReadTree(tree string, c Commit) (resultTree string, conflict bool, err error) {
	idx, err := os.CreateTemp("", "git-sd-simulate-index-*")
	if err != nil {
		return "", false, fmt.Errorf("could not create scratch index: %w", err)
	}
	idxPath := idx.Name()
	idx.Close()
	// read-tree only creates a fresh index when GIT_INDEX_FILE names a path
	// that doesn't exist yet — pointed at the empty file CreateTemp just
	// made, it instead fails reading that as a corrupt/truncated index. Free
	// the name but remove the file itself, keeping the path reserved for us.
	if err := os.Remove(idxPath); err != nil {
		return "", false, fmt.Errorf("could not free scratch index path: %w", err)
	}
	defer os.Remove(idxPath)

	env := append(os.Environ(), "GIT_INDEX_FILE="+idxPath)

	readCmd := exec.Command("git", "read-tree", "-m", "--aggressive", c.Hash+"^", tree, c.Hash)
	readCmd.Env = env
	if out, err := readCmd.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("git read-tree failed unexpectedly at %s: %w\n%s", c.Hash, err, out)
	}

	lsCmd := exec.Command("git", "ls-files", "-u")
	lsCmd.Env = env
	unmerged, err := lsCmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("git ls-files -u failed at %s: %w", c.Hash, err)
	}
	if strings.TrimSpace(string(unmerged)) != "" {
		return "", true, nil
	}

	writeCmd := exec.Command("git", "write-tree")
	writeCmd.Env = env
	out, err := writeCmd.Output()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		stderr := ""
		if ok {
			stderr = string(exitErr.Stderr)
		}
		return "", false, fmt.Errorf("git write-tree failed unexpectedly at %s: %w\n%s", c.Hash, err, stderr)
	}
	return strings.TrimSpace(string(out)), false, nil
}

var (
	gitVersionOnce  sync.Once
	gitVersionMajor int
	gitVersionMinor int
	gitVersionPatch int
	gitVersionErr   error
)

var gitVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// gitVersion returns the installed git's (major, minor, patch), memoized for
// the life of the process — it's a fixed fact about the environment, not
// worth re-execing 'git --version' on every call.
//
// 'git --version' prints one line, "git version <token>[ (extra)]", where
// <token> is dot-separated and may carry trailing platform/build metadata
// past the first three numeric components (e.g. "2.30.2.windows.1", or
// Apple's "2.39.3 (Apple Git-146)") — gitVersionPattern anchors on just the
// first three numeric components and ignores the rest.
func gitVersion() (major, minor, patch int, err error) {
	gitVersionOnce.Do(func() {
		out, cmdErr := exec.Command("git", "--version").Output()
		if cmdErr != nil {
			gitVersionErr = cmdErr
			return
		}
		token := strings.TrimSpace(string(out))
		token = strings.TrimPrefix(token, "git version ")
		m := gitVersionPattern.FindStringSubmatch(token)
		if m == nil {
			gitVersionErr = fmt.Errorf("could not parse git version from %q", strings.TrimSpace(string(out)))
			return
		}
		gitVersionMajor, _ = strconv.Atoi(m[1])
		gitVersionMinor, _ = strconv.Atoi(m[2])
		gitVersionPatch, _ = strconv.Atoi(m[3])
	})
	return gitVersionMajor, gitVersionMinor, gitVersionPatch, gitVersionErr
}

// atLeast reports whether (major, minor, patch) >= (wantMajor, wantMinor,
// wantPatch), compared lexicographically.
func atLeast(major, minor, patch, wantMajor, wantMinor, wantPatch int) bool {
	if major != wantMajor {
		return major > wantMajor
	}
	if minor != wantMinor {
		return minor > wantMinor
	}
	return patch >= wantPatch
}

func updateRef(ref, hash string) error {
	cmd := exec.Command("git", "update-ref", ref, hash)
	return cmd.Run()
}
