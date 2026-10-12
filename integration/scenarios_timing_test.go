package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"testing"
	"time"
)

// timedSD runs git-sd in dir with GIT_SD_TIMING=1 and returns its stdout, the
// timing report it printed on stderr, and the wall time of the whole process.
func timedSD(t *testing.T, dir string, args ...string) (stdout, report string, wall time.Duration) {
	t.Helper()
	cmd := exec.Command(gitSDPath, args...)
	cmd.Dir = dir
	cmd.Env = append(ghEnv(authorToken), "GIT_SD_TIMING=1")
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	begin := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git-sd %v failed: %v\n%s%s", args, err, out.String(), errBuf.String())
	}
	return out.String(), errBuf.String(), time.Since(begin)
}

// TestSDStatusTiming is a measurement, not a check: it builds a realistic
// stack on the real test repo and prints where `status` and `sync` spend their
// time (GIT_SD_TIMING=1). It is skipped unless GIT_SD_TEST_TIMING=1, so the
// normal suite pays nothing for it. Run it alone:
//
//	GIT_SD_TEST_TIMING=1 go test ./integration -run TestSDStatusTiming -v
func TestSDStatusTiming(t *testing.T) {
	if os.Getenv("GIT_SD_TEST_TIMING") != "1" {
		t.Skip("set GIT_SD_TEST_TIMING=1 to measure (it creates real PRs and is not part of the normal suite)")
	}
	repoDir := newTestRepo(t)

	// Two Stack Sets (3 + 2 PRs), one sub-commit on the first PR, and three
	// commits not yet tracked or assigned: every kind of row `status` shows.
	makeCommits(t, repoDir, "tma", "Timing A", 3)
	runGitSD(t, repoDir, "assign", "tma:0-2")
	makeCommits(t, repoDir, "tmb", "Timing B", 2)
	runGitSD(t, repoDir, "assign", "tmb:3-4")
	runGitSD(t, repoDir, "sync", "tma")
	runGitSD(t, repoDir, "sync", "tmb")
	makeCommits(t, repoDir, "tmf", "Timing Fix", 1)
	runGitSD(t, repoDir, "track")
	runGitSD(t, repoDir, "fixup", "0")
	makeCommits(t, repoDir, "tmu", "Timing Untracked", 3)

	// The flag must not change what status prints.
	plain := runGitSD(t, repoDir, "status")
	var walls []time.Duration
	for i := 0; i < 6; i++ {
		out, report, wall := timedSD(t, repoDir, "status")
		if out != plain {
			t.Fatalf("GIT_SD_TIMING changed status's output:\n--- without ---\n%s\n--- with ---\n%s", plain, out)
		}
		if !regexp.MustCompile(`(?m)^timing:$`).MatchString(report) || !regexp.MustCompile(`total`).MatchString(report) {
			t.Fatalf("no timing report on stderr:\n%s", report)
		}
		label := "warm"
		if i == 0 {
			label = "cold"
		} else {
			walls = append(walls, wall)
		}
		fmt.Printf("\n=== status (%s run %d): %v wall ===\n%s", label, i+1, wall.Round(time.Millisecond), report)
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	fmt.Printf("\n=== status wall time over %d warm runs: min %v, median %v, max %v ===\n",
		len(walls), walls[0].Round(time.Millisecond), walls[len(walls)/2].Round(time.Millisecond), walls[len(walls)-1].Round(time.Millisecond))

	// sync: first one pushes the sub-commit into the first PR, the second changes nothing.
	for i, label := range []string{"sync with a change (the sub-commit)", "sync with nothing to do"} {
		_, report, wall := timedSD(t, repoDir, "sync", "tma")
		fmt.Printf("\n=== %s (%d): %v wall ===\n%s", label, i+1, wall.Round(time.Millisecond), report)
	}
}
