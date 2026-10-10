package integration

import (
	"fmt"
	"strings"
	"testing"
)

func TestSDPRBasesOnMainWhenBaseBranchUnset(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Default Base Branch (sd.base-branch unset)
	//
	// Permanent regression guard, paired with the scenarios below: asserts
	// the standard, unconfigured path (git.ResolveBaseBranch's tiered
	// default) still works end-to-end and still produces a PR based on
	// "main" — so a change aimed at the alternate-base-branch or
	// alternate-remote paths can't silently break the common case.
	runBaseBranchScenario(t, repoDir, baseBranchScenario{
		label:       "Default Base Branch",
		stackSet:    "defaultbase",
		fileName:    "defaultbase.txt",
		commitMsg:   "Default Base Commit",
		configValue: "",
		wantBaseRef: "main",
	})
}

func TestSDPRBasesOnConfiguredNonMainBaseBranch(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Alternate Base Branch (sd.base-branch set to a real
	// non-"main" branch)
	//
	// Regression guard for internal/git.ResolveBaseBranch/ParseBaseBranch
	// and sync.go's remote-prefix stripping: sd.base-branch must work
	// end-to-end (status, assign, sync, merge) when pointed at a real branch
	// other than "main", and PRs it creates must base on that branch, not
	// "main".
	altBaseBranch := "sd-test-alt-base"

	// Best-effort: a previous interrupted run may have left this branch
	// behind (sanitizeRepo only knows about sd/-prefixed branches, not this
	// one), which would make the push below fail as a non-fast-forward.
	runGitErr(repoDir, "push", "origin", "--delete", altBaseBranch)
	runGitErr(repoDir, "branch", "-D", altBaseBranch)

	runGit(t, repoDir, "checkout", "-b", altBaseBranch)
	runGit(t, repoDir, "push", "-u", "origin", altBaseBranch)

	// Teardown regardless of outcome: return to main and remove the aux branch
	// locally and remotely. Registered before the scenario runs so it still
	// executes if runBaseBranchScenario fails via t.Fatalf.
	defer func() {
		if _, cerr := runGitErr(repoDir, "checkout", "main"); cerr != nil {
			t.Errorf("failed to check out main after alternate base branch scenario: %v", cerr)
		}
		if _, cerr := runGitErr(repoDir, "branch", "-D", altBaseBranch); cerr != nil {
			t.Errorf("failed to delete local alternate base branch: %v", cerr)
		}
		if _, cerr := runGitErr(repoDir, "push", "origin", "--delete", altBaseBranch); cerr != nil {
			t.Errorf("failed to delete remote alternate base branch: %v", cerr)
		}
	}()

	runBaseBranchScenario(t, repoDir, baseBranchScenario{
		label:       "Alternate Base Branch",
		stackSet:    "basechk",
		fileName:    "altbase.txt",
		commitMsg:   "Alt Base Commit",
		configValue: "origin/" + altBaseBranch,
		wantBaseRef: altBaseBranch,
	})
}

func TestSDWorksWhenRemoteIsNotNamedOrigin(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	// Scenario: Renamed Remote (no hardcoded "origin" assumption)
	//
	// Regression guard for internal/github.PushBranch/DeleteRemoteBranch,
	// which used to hardcode the literal "origin" as the git remote to
	// push/delete branches on. This RENAMES the sacrificial repo's actual
	// remote (not a second, identically-configured one) so any lingering
	// "origin" literal fails outright (git push would error "'origin' does
	// not appear to be a git repository") instead of silently working via a
	// same-named coincidence.
	const renamedRemote = "sdtest"
	runGit(t, repoDir, "remote", "rename", "origin", renamedRemote)
	defer func() {
		if _, rerr := runGitErr(repoDir, "remote", "rename", renamedRemote, "origin"); rerr != nil {
			t.Errorf("failed to rename remote back to \"origin\": %v", rerr)
		}
	}()

	// 3a. Explicit sd.base-branch pointed at the renamed remote.
	runBaseBranchScenario(t, repoDir, baseBranchScenario{
		label:       "Renamed Remote (explicit)",
		stackSet:    "renamedremote",
		fileName:    "renamedremote.txt",
		commitMsg:   "Renamed Remote Commit",
		configValue: renamedRemote + "/main",
		wantBaseRef: "main",
	})

	// 3b. Deterministic-default tier 2: sd.base-branch UNSET, but exactly
	// one remote (the renamed one) is configured — configuredBaseBranch must
	// infer it rather than falling back to a now-nonexistent "origin".
	runBaseBranchScenario(t, repoDir, baseBranchScenario{
		label:       "Renamed Remote (inferred default)",
		stackSet:    "renamedremotedefault",
		fileName:    "renamedremotedefault.txt",
		commitMsg:   "Renamed Remote Default Commit",
		configValue: "",
		wantBaseRef: "main",
	})

	// 3c. Deterministic-default tier 3: sd.base-branch UNSET and remotes are
	// AMBIGUOUS (no "origin", more than one remote) — must fail with a clear
	// error instead of guessing. Add a second, throwaway remote pointing at
	// the same URL so there's no ambiguity about which repo it targets, only
	// about which remote name to prefer.
	remoteURL := runGit(t, repoDir, "remote", "get-url", renamedRemote)
	runGit(t, repoDir, "remote", "add", "sdtest2", strings.TrimSpace(remoteURL))
	defer func() {
		if _, derr := runGitErr(repoDir, "remote", "remove", "sdtest2"); derr != nil {
			t.Errorf("failed to remove throwaway second remote: %v", derr)
		}
	}()

	ambigOut, ambigErr := runGitSDErr(repoDir, "status")
	switch {
	case ambigErr == nil:
		t.Fatalf("expected git sd status to refuse to guess with 2 ambiguous remotes and sd.base-branch unset, but it succeeded. Output:\n%s", ambigOut)
	case !strings.Contains(ambigOut, "no remote could be inferred"):
		t.Fatalf("expected the ambiguous-remote refusal message, got:\n%s", ambigOut)
	default:
		fmt.Println("Ambiguous remotes verified: git-sd correctly refuses to guess and asks for sd.base-branch.")
	}

	fmt.Println("Renamed remote scenario verified: git-sd correctly used the renamed remote, not a hardcoded \"origin\".")
}
