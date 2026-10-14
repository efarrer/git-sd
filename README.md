# Stacked Diff CLI Tool (git-sd)

`git-sd` is a command-line tool for stacked diffs on GitHub: it turns a large change into a chain of small, dependent pull requests and lets you keep working on top of them while they are reviewed. It does the same for several unrelated pieces of work at once, all on your one local branch (main/master): a feature, an unrelated bug fix and a side project each become their own independent stack of PRs, so you never create, switch or rebase branches to keep them apart.

## Why git-sd

- **Stacks.** A big change becomes a chain of small, reviewable PRs, each building on the one before. You keep coding on top of PRs that are still under review, and you address review feedback with `git sd fixup` instead of juggling branches.
- **Streams.** You group commits into named *Stack Sets* with `git sd assign`, and each Stack Set is its own independent stack of PRs. A feature, an unrelated bug fix and a side project can all be in flight at once on your one local branch, and be reviewed, reordered and merged in any order. Your local branch is never pushed: git-sd creates and names the PR branches for you.

### An urgent fix in the middle of a feature

Without git-sd you stash, branch, fix, push, open a PR, switch back, unstash, and rebase later:

```bash
git stash
git switch -c hotfix origin/main
# ...fix, commit...
git push -u origin hotfix && gh pr create
git switch feature && git stash pop
```

With git-sd everything stays on your one branch:

```bash
git commit -m "Fix nil check in loader"   # lands next to your feature commits
git sd assign hotfix:3                    # the index comes from `git sd status`
git sd sync hotfix                        # a separate PR, based on main
```

This works because the two pieces of work are independent, and git-sd checks that when you assign: it refuses a Stack Set whose commits depend on another set's change (or on an unassigned commit), so commits that depend on each other belong in the same Stack Set.

## Quick Start

### Prerequisites
- **GitHub CLI (`gh`):** Installed and authenticated (`gh auth login`). git-sd tells you if it isn't, the first time a command needs it.
- **Git:** Any version works. Git >= 2.38 is used when available (real content-based rename detection for stack-set-assignment validation); older git falls back to an equivalent plumbing-based check with a minor rename-detection fidelity gap — see DEVELOPMENT.md's "Git Version Compatibility."

### Installation
With Go (1.26.2 or newer):
```bash
go install github.com/efarrer/git-sd/cmd/git-sd@latest
```
This puts `git-sd` in `$(go env GOBIN)` (by default `~/go/bin`; make sure that
directory is on your `PATH`), and git then runs it as `git sd`. From a checkout,
`make build` builds `./git-sd`.

### Help
There is no man page; the binary prints everything itself: `git sd -h` is the
short summary, `git sd help` (or `git-sd --help`) the full manual, and
`git sd --agent` the complete reference for scripts and agents. (`git sd --help`
does not work: git handles `--help` for its subcommands itself, by looking for a
man page.)

### Basic Workflow
1. **Assign:** Group commits into a Stack Set: `git sd assign feat-a:0-2` (`git sd status` shows the indices; commits get their `git-sd-id` trailer automatically, here or in `move` and `fixup`).
2. **Sync:** Create/Update GitHub PRs: `git sd sync feat-a`.
3. **Address PR feedback:** Make the fix (it lands at `HEAD`), then `git sd fixup <target-index>` to attach it as the next sub-commit of that PR — no index/rank math required.
4. **Merge:** Merge approved PRs in the stack on GitHub: `git sd merge feat-a` (your local branch is untouched).
5. **Reconcile:** after PRs land (via `git sd merge` or GitHub's UI), `git sd rebase` drops the landed commits from your branch and retires their tracking; the next `git sd sync` deletes their remote branches. Safe to run at any point, even mid-stack, since a Stack Set can keep growing after a partial merge (see SPEC.md's "Living Stack Sets"). `git sd rebase --rollback` undoes it.
6. **Watch (optional):** `git sd watch` follows your PRs on GitHub and tells you when one changes (CI finished or failed, approved, conflict, merged), with a bell and an optional desktop notification (`sd.watch.notify-command`). It is read-only and runs until you press Ctrl-C. Try the notification setting without waiting for a real change with `git sd notify-test` (it lists the `GIT_SD_*` variables your command receives; `--from approved --to merged` picks the change to simulate).

   Desktop notification settings (use `git config --global` to set one for every repository). The macOS one has been run and confirmed to show a notification; the Linux and Windows ones use each platform's standard tool but have not:

   - macOS: `git config sd.watch.notify-command 'osascript -e "on run argv" -e "display notification (item 1 of argv) with title (item 2 of argv)" -e "end run" "$GIT_SD_MESSAGE" "$GIT_SD_TITLE"'`
   - Linux: `git config sd.watch.notify-command 'notify-send "$GIT_SD_TITLE" "$GIT_SD_MESSAGE"'`
   - Windows (in Git Bash, after a one-time `Install-Module BurntToast` in PowerShell): `git config sd.watch.notify-command "powershell.exe -NoProfile -Command 'New-BurntToastNotification -Text \$env:GIT_SD_TITLE, \$env:GIT_SD_MESSAGE'"`

## Documentation Structure

- **[SPEC.md](./SPEC.md):** The primary behavioral specification and CLI contract. Use this as the source of truth for features and commands.
- **[DESIGN.md](./DESIGN.md):** Architectural decisions, metadata schema, and internal implementation strategies.
- **[DEVELOPMENT.md](./DEVELOPMENT.md):** Integration testing strategy, sacrificial repository setup, and simulation scenarios.
- **[AGENTS.md](./AGENTS.md):** Context configuration for LLM-based development assistants.

## Development
- **Build:** `make build`
- **Test:** `make test` (Requires `GIT_SD_TEST_REPO` pointing to a GitHub repository URL and containing a `.git-sd-test-marker` file)
- **Simulation:** `./sd-sim.sh`
