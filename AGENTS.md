# Agent Context

This repository contains the `git-sd` tool, a stacked diff CLI for GitHub.

## Context Files
- [SPEC.md](./SPEC.md): The primary behavioral specification and CLI contract.
- [DESIGN.md](./DESIGN.md): Architectural decisions and internal implementation details.
- [DEVELOPMENT.md](./DEVELOPMENT.md): Integration testing strategy and UX simulation details.
- [Validation Skill](./skills/validation/SKILL.md): Automated validation runbook and loop logic.
- [BUGS.md](./BUGS.md): Structured ledger for tracking and resolving bugs.
- [TASKS.md](./TASKS.md): Work that is left to do.

## Instructions
1.  **Context First:** Before performing any tasks, read the context files (`SPEC.md`, `DESIGN.md`, `DEVELOPMENT.md`, `TASKS.md`, `BUGS.md`) to fully understand the project's behavioral contract, architecture, and current status.
2.  **Documentation First:** When given a direction, first determine which context files it belongs to and update those files (including `sd-sim.sh` if the UX is affected) before proceeding with implementation.
3.  **Spec First:** Always treat context files as the behavioral source of truth. Changes to functionality must be in lines with the context files.
6.  **Simulation Consistency:** Every code change that impacts the user experience MUST be reflected in `sd-sim.sh`.
7.  **Disposability:** Treat generated code as ephemeral. If a fix is needed, update the spec/design/docs and regenerate rather than just patching.
8.  **Verification:** Always run the integration tests and simulation scenarios defined in `DEVELOPMENT.md` to validate the implementation.
9.  **Autonomous Validation:** When instructed to "Validate Implementation," strictly follow the "Validation Loop" defined in [skills/validation/SKILL.md](./skills/validation/SKILL.md), logging to and fixing based on `BUGS.md`. Always fix "OBVIOUS" bugs autonomously and pause for "AMBIGUOUS" items.


