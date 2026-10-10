package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MergeTrees three-way merges two commit-ish values against a common base and
// returns the resulting tree. conflict is true if the merge cannot be done
// cleanly. It never touches the working tree or the real index.
//
// git >= 2.38 uses 'git merge-tree --write-tree'. Older git falls back to
// 'read-tree -m --aggressive' plus a per-file content merge: read-tree alone
// only resolves whole-file differences, so it would report a conflict
// whenever both sides touched the same file even in different hunks.
func MergeTrees(base, ours, theirs string) (tree string, conflict bool, err error) {
	if major, minor, patch, verr := gitVersion(); verr == nil && atLeast(major, minor, patch, 2, 38, 0) {
		return mergeTreesMergeTree(base, ours, theirs)
	}
	return mergeTreesReadTree(base, ours, theirs)
}

func mergeTreesMergeTree(base, ours, theirs string) (string, bool, error) {
	cmd := exec.Command("git", "merge-tree", "--write-tree", "--merge-base="+base, ours, theirs)
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
	return "", false, fmt.Errorf("git merge-tree failed unexpectedly: %w\n%s", cmdErr, stderr)
}

func mergeTreesReadTree(base, ours, theirs string) (string, bool, error) {
	idx, err := os.CreateTemp("", "git-sd-merge-index-*")
	if err != nil {
		return "", false, fmt.Errorf("could not create scratch index: %w", err)
	}
	idxPath := idx.Name()
	idx.Close()
	// read-tree only creates a fresh index when GIT_INDEX_FILE names a path
	// that doesn't exist yet (see simulateOneCommitReadTree).
	if err := os.Remove(idxPath); err != nil {
		return "", false, fmt.Errorf("could not free scratch index path: %w", err)
	}
	defer os.Remove(idxPath)
	env := append(os.Environ(), "GIT_INDEX_FILE="+idxPath)

	run := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return out, nil
	}

	if _, err := run(nil, "read-tree", "-m", "--aggressive", base, ours, theirs); err != nil {
		return "", false, err
	}
	unmerged, err := run(nil, "ls-files", "-u", "-z")
	if err != nil {
		return "", false, err
	}

	type stage struct{ mode, sha string }
	byPath := map[string]map[string]stage{}
	var order []string
	for _, rec := range strings.Split(string(unmerged), "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return "", false, fmt.Errorf("unexpected ls-files -u record %q", rec)
		}
		if byPath[path] == nil {
			byPath[path] = map[string]stage{}
			order = append(order, path)
		}
		byPath[path][fields[2]] = stage{mode: fields[0], sha: fields[1]}
	}

	for _, path := range order {
		st := byPath[path]
		s1, s2, s3 := st["1"], st["2"], st["3"]
		// Added/deleted on one side, or added on both: a real conflict.
		if s1.sha == "" || s2.sha == "" || s3.sha == "" {
			return "", true, nil
		}
		if s2.mode != s3.mode {
			return "", true, nil
		}
		merged, clean, err := mergeBlobs(s2.sha, s1.sha, s3.sha)
		if err != nil {
			return "", false, err
		}
		if !clean {
			return "", true, nil
		}
		if _, err := run(nil, "update-index", "--force-remove", "--", path); err != nil {
			return "", false, err
		}
		if _, err := run(nil, "update-index", "--add", "--cacheinfo", s2.mode+","+merged+","+path); err != nil {
			return "", false, err
		}
	}

	out, err := run(nil, "write-tree")
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(out)), false, nil
}

// mergeBlobs three-way merges file contents (current, base, other) with
// 'git merge-file' and stores the clean result as a blob.
func mergeBlobs(currentSHA, baseSHA, otherSHA string) (blob string, clean bool, err error) {
	dir, err := os.MkdirTemp("", "git-sd-merge-file-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	write := func(name, sha string) (string, error) {
		out, err := exec.Command("git", "cat-file", "blob", sha).Output()
		if err != nil {
			return "", fmt.Errorf("git cat-file blob %s: %w", sha, err)
		}
		path := dir + "/" + name
		return path, os.WriteFile(path, out, 0600)
	}
	cur, err := write("current", currentSHA)
	if err != nil {
		return "", false, err
	}
	base, err := write("base", baseSHA)
	if err != nil {
		return "", false, err
	}
	other, err := write("other", otherSHA)
	if err != nil {
		return "", false, err
	}
	out, mergeErr := exec.Command("git", "merge-file", "-p", cur, base, other).Output()
	if mergeErr != nil {
		if exitErr, ok := mergeErr.(*exec.ExitError); ok && exitErr.ExitCode() > 0 {
			return "", false, nil // exit code = number of conflicts
		}
		return "", false, fmt.Errorf("git merge-file: %w", mergeErr)
	}
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(out)
	hashed, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("git hash-object: %w", err)
	}
	return strings.TrimSpace(string(hashed)), true, nil
}

// Author identifies who wrote a commit, for CreateCommitAs.
type Author struct{ Name, Email, Date string }

// GetAuthor returns the author of a commit.
func GetAuthor(hash string) (Author, error) {
	out, err := exec.Command("git", "log", "-1", "--format=%an%x00%ae%x00%aI", hash).Output()
	if err != nil {
		return Author{}, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "\x00")
	if len(parts) != 3 {
		return Author{}, fmt.Errorf("unexpected author record for %s", hash)
	}
	return Author{Name: parts[0], Email: parts[1], Date: parts[2]}, nil
}

// CreateCommitAs creates a commit object with the given tree, parent and
// message, authored as author (the committer stays the current user).
func CreateCommitAs(tree, parent, message string, author Author) (string, error) {
	cmd := exec.Command("git", "commit-tree", tree, "-p", parent, "-m", message)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+author.Date,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffShortstat returns 'git diff --shortstat a b' without surrounding space
// (e.g. "1 file changed, 4 insertions(+)"), or "" if there is no difference.
func DiffShortstat(a, b string) string {
	out, err := exec.Command("git", "diff", "--shortstat", a, b).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
