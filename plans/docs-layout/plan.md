# Documentation layout
## Goal
Keep `README.md` and `AGENTS.md` at the repository root, plans under `plans/`, and all other documentation under `docs/`. Source code, configuration, examples, scripts, and `LICENSE` stay in place.

## Plan
- Move the security policy, deployment overview, and Docker test guide into `docs/`.
- Move contribution and release instructions out of the root README into task guides.
- Add a documentation index organized by reader need. Do not add empty Diátaxis categories.
- Update links and record the layout in `AGENTS.md`. Preserve existing plans as historical records.

## Diátaxis review
Separated installation behavior and client/runtime contracts into reference pages. Kept deployment, installation, testing, and release tasks in how-to guides. Kept design context and the threat model in explanation. Policy and agent instructions remain project guidance. No tutorial was added because there is no verified guided lesson.

Removed the obsolete `config diff` instruction, qualified approval-cache behavior during Telegram outages, and removed unverified Talos DNS retry claims. Existing hardware-validation warnings remain.

## Verification
- `make test`: passed, including race detection.
- Markdown placement: passed; only `README.md` and `AGENTS.md` remain outside `docs/` and `plans/`.
- Local Markdown link targets: passed.
- Obsolete documentation paths and `config diff` outside historical plans: none.
- `git diff --check`: passed.
- No runtime files changed. Docker E2E was not needed for documentation-only work.

## Implementation consistency audit
Reviewed every page under `docs/` and the root entry points against commit `192bf25` in this worktree. Runtime code remains unchanged.

| Area | Implementation evidence | Documentation correction |
|---|---|---|
| CLI and config | `cmd/azath/client.go`, `seal.go`, config commands; `internal/config/client.go`, `validate.go` | Validated all three complete azath YAML examples; documented the rejection of zero cache TTL, endpoint syntax, and client TLS requirements |
| Device authority | `cmd/azath/serve.go`, `internal/server/server.go` | Required restarts after config changes; clarified that UUIDs are not credentials and Seal has no approval check |
| Crypto and runtime | `internal/crypto/aes.go`, `internal/server/grpc.go`, `response.go`, `internal/platform/mlock_linux.go` | Corrected direct AES key use; documented gRPC size limits, nonfatal memory-lock failure, and readiness scope |
| Deployment | Shipped Caddyfile, systemd unit, and Synology helper | Added `tls internal`, moved setup after account creation, corrected ownership and backup guidance, and preserved DSM trust-store uncertainty |
| Installer and releases | `deploy/scripts/install.sh`, `.goreleaser.yml`, CI and release workflows | Clarified identity-based updates and conditional rollback hints; added missing dependencies and removed shell-breaking rollback placeholders |
| External integration | Talos schema documentation and repository test boundaries | Removed an unsupported minimum-version guarantee and exact metadata-partition claim; warned that live Talos behavior and failover remain unverified |
| Security policy | No local proof of GitHub repository feature settings | Made private vulnerability reporting conditional instead of claiming it is enabled |

Validation after audit:
- `make test`: passed with race detection.
- Built the CLI and checked command help; all three complete documented azath YAML examples passed `config validate` after replacing the explicit UUID placeholder.
- Reproduced `approval_cache_ttl: 0s` rejection with the built CLI. The internal gate's zero-cache support remains an implementation/config mismatch; no behavior change was made.
- Docker unlock E2E: passed with `--network none`.
- Installer suites: Debian 13 and BusyBox 1.37 each passed 42 checks.
- Installer shell syntax, local links and anchors, and `git diff --check`: passed.
- Not validated: live Talos/DSM hardware, DSM CA installation, production systemd/Caddy execution, GitHub settings, or public release downloads. These require external environment checks.

## Skills loaded and used
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| `skill-loader` | prompt-required | Documentation changes | Selected documentation and workflow guidance |
| `diataxis` | user-requested | Documentation organization and review | Separated task guides, reference, and explanation without empty categories |
| `write` | skill-loader | Prose edits | Used direct wording and preserved validation limits |
| `codebase-research` | agent-selected | Cross-file links and command accuracy | Inventoried documentation and checked CLI and release configuration |
| `feature-worktree` | agent-selected | Keep writes off main | Reused the clean feature worktree at the fetched main commit |
