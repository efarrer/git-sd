# Agent Context

This repository contains the `git-sd` tool, a stacked diff CLI for GitHub.

## Context Files
- [SPEC.md](./SPEC.md): The primary behavioral specification and CLI contract.
- [DESIGN.md](./DESIGN.md): Architectural decisions and internal implementation details.
- [DEVELOPMENT.md](./DEVELOPMENT.md): Integration testing strategy and UX simulation details.
- [Validation Skill](./skills/validation/SKILL.md): Automated validation runbook and loop logic.
- [BUGS.md](./BUGS.md): Structured ledger for tracking and resolving bugs.

## Instructions
1.  **Documentation First:** When given a direction, first determine which context files (e.g., `SPEC.md`, `DESIGN.md`, `DEVELOPMENT.md`) it belongs to and update those files before proceeding with implementation or other actions.
2.  **Spec First:** Always treat `SPEC.md` as the behavioral source of truth. Changes to functionality must first be reflected in `SPEC.md`.
3.  **Design Alignment:** Ensure code changes align with the architectural patterns defined in `DESIGN.md`.
4.  **Code Generation:** Do not generate implementation code until explicitly instructed to do so. When directed, follow the tasks defined in [TASKS.md](./TASKS.md), generating code based on the specification and design.
4.  **Disposability:** Treat generated code as ephemeral. If a fix is needed, update the spec/design and regenerate rather than just patching.
5.  **Verification:** Always run the integration tests and simulation scenarios defined in `DEVELOPMENT.md` to validate the implementation.
6.  **Autonomous Validation:** When instructed to "Validate Implementation," strictly follow the "Validation Loop" defined in [skills/validation/SKILL.md](./skills/validation/SKILL.md), logging to and fixing based on `BUGS.md`. Always fix "OBVIOUS" bugs autonomously and pause for "AMBIGUOUS" items.


