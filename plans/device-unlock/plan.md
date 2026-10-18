# Device unlock simplification implementation plan
**Goal:** Unlock Synology shares and Talos disks after Telegram approval with one device-scoped approval window.
**Smallest user-feedback slice:** A waiting Unseal RPC completes after approval, including with caching disabled.
**Out of scope:** New wire protocols, distributed approval state, live NAS changes, publishing or pushing. Preserve endpoint support but recommend one LAN server. No compatibility layer: nothing is deployed.
**Architecture:** Config is the sole device authority. Telegram completes waiting requests and optionally caches approval. A dedicated Synology command unlocks configured shares sequentially and reports partial failure.
**Tech stack:** Existing Go, Cobra, gRPC, Telegram HTTP, YAML and POSIX boot launcher.

## Source and decisions
The user approved device-wide approval windows and replacing the generic command runner with a multi-share Synology command, and authorized completion of all review findings. This is one-shot planning from the agreed conversation, not an interactive planning gate. The existing simplify-consistency plan is historical and remains unchanged.

- Remove registry persistence, HMAC state, reconciliation, notifier scaffolding, compatibility aliases, unused diff and duplicate client schema.
- Preserve blob format, TLS termination, config disablement and Talos random-failure response contract.
- Decrypt/validate before prompting; do not retain plaintext while waiting if decrypting again after approval avoids that ownership cost.
- Approval completes the current request independently of the optional reuse window. TTL zero disables reuse only.
- Denial, timeout, API failure, close and cancellation fail closed. Callbacks remain bound to pending request identity.
- Multiple requests for one UUID share one pending prompt. Bound pending requests, waiter growth and HTTP sends; enforce configured prompt rate limit after denial and API errors too.
- Synology config lists share names and sealed-blob paths. Invoke the fixed DSM synoshare executable directly, no generic command or shell. Mount each share at most once per invocation; continue on share failure. Do not log passphrases or subprocess output that could expose them.
- RPC deadlines must permit human approval and still be bounded. Preserve per-endpoint failover on transport failure only; do not retry a mount across endpoints.
- Transfer plaintext ownership instead of copying. Zero server response buffers only after serialization, using a supported gRPC codec seam if verified; do not clear data before marshaling.

## Discovery and feasibility
| Requirement | Mechanism/evidence | Verification | If unavailable |
|---|---|---|---|
| Config-only authorization | Existing devices map in internal/server/server.go | Configured device works without any state file; rename works | Block |
| Waiting approval | Existing Telegram pending map, HTTP callbacks and contexts | Gate tests with fake Telegram plus gRPC integration | Block |
| Synology multi-share | Existing exec.CommandContext and fake mount Docker seam | CLI tests and network-isolated Docker suite | Report DSM compatibility limit |
| Response cleanup | Inspect gRPC codec API in pinned grpc dependency | Wire response remains correct and source buffer is cleared | Document precise residual, do not invent unsafe hooks |

## Skills loaded and used
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | prompt-required | Lifecycle | Route guidance |
| feature-worktree | prompt-required | Writes from main | Dedicated feature worktree from origin/main |
| codebase-research | prompt-required | Architecture change | Trace existing gate, client, registry and tests |
| go-best-practices | skill-loader | Go | Ownership, bounded concurrency, tests |
| cli-best-practices | skill-loader | Synology command | Explicit inputs, safe errors and partial results |
| script-best-practices | skill-loader | Boot helper | Thin bounded launcher and shell checks |
| write | skill-loader | Docs | Concrete contracts |
| learning-lookup | prompt-required | Planning | Advisory lookup only; unavailable source does not block |

## Implementation contract
| Component | Files | Responsibility | Verification |
|---|---|---|---|
| Approval | internal/gate/gate.go, internal/gate/telegram.go, gate tests | Waiters, callback binding, reuse, rate limits, cleanup | go test ./internal/gate -race -count=1 |
| Server | internal/server/*, cmd/azath/serve.go, serve tests | Config policy, response safety, error propagation | go test ./internal/server ./cmd/azath -race -count=1 |
| Config | internal/config/*, examples/* | Remove obsolete schema; one client schema | Config/CLI tests |
| Synology | cmd/azath/client.go, cmd/azath/seal.go, cmd/azath/main.go, client tests, deploy/synology/azath-unseal.sh | Sequential unlock with partial results | CLI and Docker tests |
| Operations | internal/observability/*, alerts/*, docs/*, README.md, AGENTS.md, tests/docker/* | Accurate metrics, docs, guidance and integration | Full checks |

Safety: homelab scale, few configured devices and shares; explicit finite input and concurrency limits. No secret-bearing logs or unbounded goroutines. Server owns gate shutdown and secrets; request owns plaintext; caller owns decoded client response. Nil gate denies. No new dependencies unless unavoidable.
Observability: retain meaningful request/outcome metrics, remove registry/notifier-only metrics and alerts, report share outcomes without secrets. Unexpected server failure must return nonzero after cleanup.
Rollout/rollback: no deployed state to migrate. User reviews and builds the local branch; reverting the feature restores the old contract. Never access a real NAS, Telegram bot or secret manager in tests.
Stop conditions: unavailable live DSM checks are a documented limit, not grounds to claim compatibility. Never bypass failing tests or weaken security assertions to obtain green.

## Requirements and acceptance
- R1: Given an enabled configured UUID and valid blob, when Telegram approves, the original RPC SHALL complete with plaintext. Given denial, timeout, canceled context, missing gate, invalid blob or disabled UUID, plaintext SHALL NOT be returned. Invalid blobs SHALL NOT prompt.
- R2: Given TTL zero, approval SHALL complete current waiters but SHALL NOT authorize a later request. Given positive TTL, another share/disk for that UUID SHALL reuse approval only within the window. Other UUIDs SHALL NOT reuse it.
- R3: Given repeated requests or API failures, prompt rate and concurrent work SHALL remain bounded. Stale/wrong-user/wrong-chat/tampered callbacks SHALL NOT approve replacements. Close SHALL drain work and clear secrets.
- R4: Given device config, startup SHALL require no registry state; renaming a device SHALL work. Historical registry fields SHALL be rejected rather than silently honored.
- R5: Given multiple Synology shares, one invocation SHALL attempt each share once after Unseal; one failed mount SHALL NOT prevent other shares. It SHALL return nonzero on partial failure and SHALL NOT expose plaintext or execute arbitrary commands. Invalid config SHALL fail before mounts.
- R6: Given unexpected server failure, shutdown SHALL return the error; normal cancellation SHALL return success. Ordinary and panic Unseal failures SHALL have the same expected response size. Plaintext buffers SHALL have explicit cleanup ownership.
- R7: Docs, examples, boot helper, tests, metrics and AGENTS.md SHALL describe only supported behavior; obsolete aliases, registry, notifier, duplicate types and unused diff SHALL be removed.

## Execution tasks
This is one integration unit: registry removal and client replacement change shared signatures and fixtures across packages, so intermediate mechanical batches need not compile. Validate focused behavior before the final full gate. No implementation commits or PR required; leave code uncommitted for review.

### Task 1: Complete approved Unseal requests (R1-R3, R6)
- [ ] Add focused failing tests for zero-cache waiting approval, concurrent requests, cancellation, rate limits, malformed blob suppression and missing gate.
- [ ] Implement bounded request completion and reuse; preserve callback identity checks and secret cleanup.
- [ ] Share failure-response construction, verify transport-safe cleanup and propagate serve errors.
- [ ] Run gate/server/serve tests with race detection.

### Task 2: Remove redundant runtime state (R4, R7)
- [ ] Map registry, notifier, aliases, Diff and duplicate client types with semantic references before deletion.
- [ ] Remove runtime and schema dependencies, obsolete files/tests/metrics/alerts; migrate all real consumers.
- [ ] Add config-only rename/startup tests and run config/server/CLI tests.

### Task 3: Ship multi-share Synology operation (R5)
- [ ] Add failing public-command tests for multiple shares, partial failure, invalid input, canceled approval and no repeated mounts.
- [ ] Replace generic command runner with dedicated Synology command and shared client config. Keep seal using the same identity/endpoints schema.
- [ ] Simplify boot helper to one invocation and update helper tests.
- [ ] Migrate Docker fake mount and Telegram suite to approval of the original RPC and multi-share operation.

### Task 4: Documentation and complete verification (R1-R7)
- [ ] Update README.md, docs/architecture/mvp.md, installation/runbooks, examples and AGENTS.md. Inspect all remaining references including deployment docs and CI.
- [ ] Run make test, make vet, golangci-lint when available, go fix -diff ./..., LSP diagnostics and git diff --check.
- [ ] Run shell syntax/ShellCheck, Docker build and network-none Docker E2E, installer tests in Debian and BusyBox when Docker is available.
- [ ] Review final diff against all eight findings and seven requirements. Record exact results and any unverified live-platform limits below.

## Execution evidence
Pending.
