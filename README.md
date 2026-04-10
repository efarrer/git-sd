# Stacked Diff CLI Tool (git-sd)

`git-sd` is a command-line tool designed to enable a "stacked diff" workflow with GitHub, allowing developers to manage a series of dependent pull requests efficiently on a single branch.

## Quick Start

### Prerequisites
- **GitHub CLI (`gh`):** Installed and authenticated (`gh auth status`).
- **Git:** Version 2.30 or higher.

### Installation
```bash
make build
# Move the 'git-sd' binary to your PATH
```

### Basic Workflow
1. **Status:** Run `git sd status` to see your commits. The tool will automatically add `git-sd-id` trailers.
2. **Assign:** Group commits into a Stack Set: `git sd assign feat-a:0-2`.
3. **Sync:** Create/Update GitHub PRs: `git sd sync feat-a`.
4. **Merge:** Safely merge approved PRs in the stack: `git sd merge feat-a`.

## Documentation Structure

- **[SPEC.md](./SPEC.md):** The primary behavioral specification and CLI contract. Use this as the source of truth for features and commands.
- **[DESIGN.md](./DESIGN.md):** Architectural decisions, metadata schema, and internal implementation strategies.
- **[DEVELOPMENT.md](./DEVELOPMENT.md):** Integration testing strategy, sacrificial repository setup, and simulation scenarios.
- **[AGENTS.md](./AGENTS.md):** Context configuration for LLM-based development assistants.

## Development
- **Build:** `make build`
- **Test:** `make test` (Requires `GIT_SD_TEST_REPO` pointing to a GitHub repository URL and containing a `.git-sd-test-marker` file)
- **Simulation:** `./sd-sim.sh`
