# Consistency simplification pass
## Requirements
Review the repository for clarity and consistency. Make bounded simplifications only where existing tests cover behavior. Preserve behavior, sequencing, errors, security, and observability. Do not add features, abstractions, or configuration.

## Scope and evidence
The repository-wide review permission permits inspection beyond a recent diff. This first pass inspected config parsing and validation, CLI config editing and client operations, KMS request handling, Telegram approval, key management, cryptography, registry persistence, and health checks. It is not an exhaustive audit of every file.

Apply two independent changes:
- Replace the single-entry master-key-source lookup map with comparison against MasterKeySourceFile. Preserve the error text and early return for invalid sources.
- Remove the internal checkResult wrapper. Return a boolean from runCheck; preserve panic recovery, error logs, concurrent check execution, and HTTP status/body values.

Leave registry semantics, compatibility aliases, CLI schema duplication, cryptographic implementation, and Telegram state management unchanged. These are not safe targets for a mechanical consistency pass.

## Verification
Before and after edits, run `go test ./internal/config ./internal/observability -race -count=1` and `make test`. Run `go vet ./...`, `go fix -diff ./...`, LSP diagnostics, and `git diff --check` after edits. Existing health tests cover success, error, panic, empty registration, opaque responses, and error logging. Existing config tests cover file and unsupported master-key sources.

## Ledger
- Started from clean origin/main at 978b19ecb7db4c95416dd27a39cca608d1727be9 in the dedicated maruina/simplify-consistency worktree.
- Baseline focused tests and full race-enabled suite passed.
- Simplification applied: removed the single-entry source map and internal health-result wrapper. No public API, response format, error text, logging, or execution order was changed.
- Post-edit focused tests, `make test`, `go vet ./...`, LSP diagnostics, and `git diff --check` passed. `go fix -diff ./...` returned 1 with suggestions only in untouched client, secret, and Telegram files; no suggestions were applied.
- Docker E2E was not run: no Telegram, client/server wiring, endpoint, or Synology helper code changed.
- The branch has no configured upstream or open PR. Leave the simplification commit local.

## Skills loaded and used
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | prompt-required | Simplification workflow | Selected applicable guidance before code work |
| feature-worktree | prompt-required | Base checkout on main | Created a separate feature worktree from fetched origin/main |
| codebase-research | skill-loader | Unfamiliar repository areas | Mapped flows, existing patterns, callers, and test coverage before edits |
| go-best-practices | skill-loader | Go implementation changes | Removed unnecessary internal representations and preserved synchronization and errors |
| write | skill-loader | Plan, comments, and handoff | Kept the evidence and scope limits explicit |
