# Stacked Diff CLI Tool (git-sd)

`git-sd` is a command-line tool designed to enable a "stacked diff" workflow with GitHub, allowing developers to manage a series of dependent pull requests efficiently on a single branch.

## Quick Start

### Prerequisites
- **GitHub CLI (`gh`):** Installed and authenticated (`gh auth login`). git-sd tells you if it isn't, the first time a command needs it.
- **Git:** Any version works. Git >= 2.38 is used when available (real content-based rename detection for stack-set-assignment validation); older git falls back to an equivalent plumbing-based check with a minor rename-detection fidelity gap — see DEVELOPMENT.md's "Git Version Compatibility."

### Installation
```bash
make install
# Installs the binary and man page under /usr/local by default.
# Override the location with: make install PREFIX=~/.local
```
This also makes `git sd --help` work (it opens the installed man page, same
as `git commit --help`); `git sd -h` and `git sd --agent` work either way,
without installing anything, since they're printed directly by the binary.

### Basic Workflow
1. **Track:** Run `git sd track` to add `git-sd-id` trailers to new commits (or just run `git sd status` first — it's read-only and shows which commits still need tracking).
2. **Assign:** Group commits into a Stack Set: `git sd assign feat-a:0-2`.
3. **Sync:** Create/Update GitHub PRs: `git sd sync feat-a`.
4. **Address PR feedback:** Make the fix (it lands at `HEAD`), then `git sd fixup <target-index>` to attach it as the next sub-commit of that PR — no index/rank math required.
5. **Merge:** Merge approved PRs in the stack on GitHub: `git sd merge feat-a` (your local branch is untouched).
6. **Reconcile:** after PRs land (via `git sd merge` or GitHub's UI), `git sd rebase` drops the landed commits from your branch and retires their tracking; the next `git sd sync` deletes their remote branches. Safe to run at any point, even mid-stack, since a Stack Set can keep growing after a partial merge (see SPEC.md's "Living Stack Sets"). `git sd rebase --rollback` undoes it.
7. **Watch (optional):** `git sd watch` follows your PRs on GitHub and tells you when one changes (CI finished or failed, approved, conflict, merged), with a bell and an optional desktop notification (`sd.watch.notify-command`). It is read-only and runs until you press Ctrl-C. Try the notification setting without waiting for a real change with `git sd notify-test` (it lists the `GIT_SD_*` variables your command receives; `--from approved --to merged` picks the change to simulate).

   Desktop notification settings (use `git config --global` to set one for every repository). The macOS one has been run by the author; the Linux and Windows ones use each platform's standard tool but have not:

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
