package main

import (
	"fmt"
	"git-sd/internal/timing"
	"os"
)

func main() {
	defer timing.Report(os.Stderr) // GIT_SD_TIMING=1; exits go through exitCleanly, which reports too
	if len(os.Args) < 2 {
		showUsage()
		exitCleanly(0)
	}

	command := os.Args[1]
	switch command {
	case "track", "status", "assign", "sync", "move", "fixup", "merge", "rebase":
		if reconcilePending() {
			if command != "status" {
				fmt.Printf("Error: %s\n", pendingInProgressMsg)
				exitCleanly(1)
			}
			fmt.Printf("Note: %s.\n", pendingInProgressMsg)
		}
	}
	switch command {
	case "track":
		runTrack(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "assign":
		runAssign(os.Args[2:])
	case "sync":
		runSync(os.Args[2:])
	case "move":
		runMove(os.Args[2:])
	case "fixup":
		runFixup(os.Args[2:])
	case "merge":
		runMerge(os.Args[2:])
	case "rebase":
		runRebase(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "help", "--help", "-h":
		showUsage()
	case "--agent":
		showAgentUsage()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		showUsage()
		exitCleanly(1)
	}
}

func showUsage() {
	fmt.Print(`usage: git sd <command> [<args>]

Commands:
  track       Add git-sd-id trailers to untracked commits
  status      List commits and their Stack Set/PR status
                Options: --legend, --no-legend
  assign      Assign commits to a Stack Set
  move        Reorder commits, promote/demote/reparent sub-commits
  fixup       Attach HEAD as the next sub-commit of a PR (PR feedback)
  sync        Synchronize GitHub PR state with local branch
  merge       Safely merge approved PRs in a stack
                Options: --all, --upto <index> (merge only the bottom prefix up
                to that status index; a prefix, never a cherry-picked middle PR)
  rebase      Drop landed PRs' commits and rebase onto the updated base
              branch (local only; backed up, auto-rolls back on conflict)
                Options: --rollback [<n>] (restore backup n, 1 = newest),
                --list (show backups)
  watch       Watch the PRs' GitHub state and notify when it changes (runs
              until you stop it with Ctrl-C; read-only)

Options:
  --help      Show this help message
  --agent     Print a detailed, complete command reference for agents

Configuration (via git config):
  sd.status.legend [true|false]      Always show the status emoji legend (Default: false)
  sd.status.columns [string]         Comma-separated list of columns for 'git sd status'
                                     (Default: Icon,Status,Index,Stack Set,git-sd-id,Hash,Message,PR)
  sd.status.color [true|false]       Color the Status word (Default: true; terminal only,
                                     off if NO_COLOR is set or TERM=dumb)
  sd.watch.notify-command [string]   Shell command run for each change 'git sd watch' sees
                                     (details in GIT_SD_* environment variables; see SPEC.md)
  sd.watch.min-interval [duration]   Fastest 'git sd watch' poll (Default: 5s)
  sd.watch.max-interval [duration]   Slowest poll while nothing changes (Default: 60s)
  sd.base-branch [string]            Upstream branch to target instead of main/master, as
                                     "<remote>/<branch>". The remote must be one of this
                                     repo's configured git remotes (any remote works, not
                                     just "origin"). If unset, defaults to the "origin"
                                     remote's main branch if "origin" exists, else the
                                     repo's sole remote's main branch if there's exactly
                                     one; otherwise git-sd asks you to set this explicitly.
                                     Useful for testing against a branch without main's
                                     approval guards.

Note: 'git sd merge' delegates approval/CI enforcement entirely to GitHub's
native Stacks async-merge API and whatever branch protection is configured
on the repo — git-sd does not check approvals or CI status itself.

Note: Use 'git config' for project config or 'git config --global' for home config.
`)
}
