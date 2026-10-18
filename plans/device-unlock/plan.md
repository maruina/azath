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
- [x] Add focused failing tests for zero-cache waiting approval, concurrent requests, cancellation, rate limits, malformed blob suppression and missing gate.
- [x] Implement bounded request completion and reuse; preserve callback identity checks and secret cleanup.
- [x] Share failure-response construction, verify transport-safe cleanup and propagate serve errors.
- [x] Run gate/server/serve tests with race detection.

### Task 2: Remove redundant runtime state (R4, R7)
- [x] Map registry, notifier, aliases, Diff and duplicate client types with semantic references before deletion.
- [x] Remove runtime and schema dependencies, obsolete files/tests/metrics/alerts; migrate all real consumers.
- [x] Add config-only rename/startup tests and run config/server/CLI tests.

### Task 3: Ship multi-share Synology operation (R5)
- [x] Add public-command tests for multiple shares, partial failure, invalid input, canceled approval and no repeated mounts. Tests are present and pass; this continuation did not reproduce a pre-implementation failure for the already implemented command.
- [x] Replace generic command runner with dedicated Synology command and shared client config. Keep seal using the same identity/endpoints schema.
- [x] Simplify boot helper to one invocation and update helper tests.
- [x] Migrate Docker fake mount and Telegram suite to approval of the original RPC and multi-share operation.

### Task 4: Documentation and complete verification (R1-R7)
- [x] Update README.md, docs/architecture/mvp.md, installation/runbooks, examples and AGENTS.md. Inspect all remaining references including deployment docs and CI.
- [x] Run make test, make vet, golangci-lint when available, go fix -diff ./..., LSP diagnostics and git diff --check.
- [x] Run shell syntax/ShellCheck, Docker build and network-none Docker E2E, installer tests in Debian and BusyBox when Docker is available.
- [x] Review final diff against all eight findings and seven requirements. Record exact results and any unverified live-platform limits below.

## Execution evidence
### Status
Tasks 1–4 are complete. The final Task 3/4 evidence below supersedes earlier stage status and checks. Existing worktree changes were preserved. No implementation commit, push, comment, PR, deployment, or live credential/service access was made. Live DSM and systemd compatibility remain unverified.

### Feasibility and gap ledger
| Item | Evidence and decision |
|---|---|
| Response cleanup seam | Pinned gRPC v1.82.0 provides `encoding.CodecV2` and `grpc.ForceServerCodecV2`. Its protobuf Marshal copies message data into independent transport buffers. A server-local wrapper clears application-owned response data after Marshal returns. Small (17-byte) and pooled (8192-byte) payload tests decode the serialized buffers after source cleanup. A normal gRPC client also verifies successful Unseal over the production server wiring. |
| Runtime copy limit | The server clears its response slice; the client takes ownership of decoded `resp.Data` without an extra copy. Protobuf serialization, transport, TLS, and client decoding can retain separate runtime-owned copies. The exec argument string and AES expanded key schedule remain outside reliable zeroing. No unsafe hooks were added. |
| Patch tool | `apply_patch` is not available in this environment. Existing files were changed with small exact `edit` replacements; new files used `write`. |
| Semantic navigation | LSP context and reference navigation were available and used for Unseal, panic recovery, NewGRPCServer, runServe, tryUnseal, and affected tests. Some initial positions were invalid; valid symbol positions were then located. |
| Test isolation gap | The existing dev-mode boot/shutdown test used the production Telegram origin with a disposable test token. Initial test runs retained that behavior. The test now supplies a local fake API; final runs use that fake. No live credentials, NAS, or 1Password were used. |

### Implemented safety changes
- Nil gate denies Unseal. Existing success and concurrency tests now pass an explicit approving gate.
- Unseal decrypts and clears a validation buffer before checking the gate, then decrypts again after approval. Corrupt and short blobs do not call the gate.
- Panic and ordinary failure responses share the expected-plaintext-length calculation. Panic coverage uses a non-32-byte plaintext.
- Server-local protobuf codec clears application response buffers after serialization. The client returns decoded data without an extra plaintext copy.
- Signal handling starts before startup work. Unexpected serve errors survive cleanup and are returned. Server token cleanup also runs after early listener failures.
- A metrics bind-error regression test verifies the returned error and released gRPC listener.

### Checks run
| Command | Result |
|---|---|
| `go test ./internal/server -run TestResponseCodecClearsSourceAfterMarshal -count=1` before implementation | Failed as expected: responseCodec undefined. |
| `go test ./internal/server ./cmd/azath -race -count=1` | Passed after safety changes and explicit approval fixtures. |
| `make test` | Passed with race detection. |
| `make vet` | Passed. |
| `make lint` | Initial run found one wrapcheck issue in the codec; fixed. Subsequent run passed with zero issues. |
| `go fix -diff ./...` | Exit 1 with proposed modernization diffs. No fixes applied; unrelated modernization is outside scope. |
| LSP diagnostics | No diagnostics for server.go, response.go, or serve.go. |
| `git diff --check` | Passed. |
| `sh -n deploy/scripts/install.sh deploy/scripts/install_test.sh` | Passed. |
| `shellcheck --shell=sh deploy/scripts/install.sh deploy/scripts/install_test.sh` | Passed. |
| `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` | Passed. |
| `docker run --rm --network none azath-docker-e2e` | Passed existing suite. This still tests the old pending/retry, registry, and generic-client contracts, not the planned replacement. |
| Debian 13 installer container command from AGENTS.md | Passed: 42 tests, zero failures. |
| BusyBox 1.37 installer container command from AGENTS.md | Passed: 42 tests, zero failures. |

Logs are in `/tmp/azath-*.log`; these are local, temporary evidence, not committed artifacts. Live DSM and systemd compatibility remain unverified.

### Remaining required work at the earlier checkpoint (historical)
- Dedicated sequential multi-share Synology command and shared client schema; fixed executable; bounded input and deadlines; safe partial failure and NUL handling; boot helper and behavior tests.
- Migrate all fixtures, Docker E2E, docs, examples, alerts, and AGENTS.md to the agreed contracts; repeat acceptance review and complete verification.

### Task 1 completion evidence
- Test first: `go test ./internal/gate -run '^TestBlockingApprovalWithoutCache$' -count=1` failed because Check returned Pending before approval. It passes with blocking completion.
- Check shares one prompt per canonical UUID. Each caller can cancel independently. The last canceled waiter removes the pending request and cancels its HTTP send. Approval completes current callers even with cache TTL zero; later callers need a fresh approval. Positive cache TTL remains device-scoped.
- Limits: 64 pending devices, 32 callers per device, 8 simultaneous HTTP sends, and 1024 entries each for prompt history and cached approval. Admission fails closed rather than queuing work. Fixed limits are an intentional homelab constraint, documented in code.
- `serve` passes the validated `notifications.telegram.rate_limit` to the gate. The interval starts when a prompt attempt is admitted, including attempts that end in denial, timeout, cancellation, or API failure. Expired history is removed on admission. Cache hits do not create prompts.
- Callbacks retain HMAC, user, chat, expiry, and request-identity checks. HTTP status errors, malformed send responses, and `ok:false` fail closed. Close rejects new calls, releases current callers, cancels and drains HTTP work and polling, clears state, and zeros the bot token and HMAC key.
- Gate fixtures now exercise blocking behavior. Coverage includes zero-cache completion, deduplication, independent and final-waiter cancellation, timeout, cache expiry and UUID isolation, prompt interval expiry, resource admission limits, hostile callbacks, replacement requests, logical API errors, polling failures, and shutdown during a blocked send.
- Docker coverage now approves the original waiting client invocation. Registry and generic client coverage remain until Tasks 2–3. No real Telegram, NAS, or 1Password endpoints were used in this execution.

| Final command/check | Result |
|---|---|
| `go test ./internal/gate -race -count=20` | Passed. A prior repeated run found a test-only race from concurrent direct and background getUpdates calls; the test now uses only background polling. |
| `go test ./internal/gate ./internal/server ./cmd/azath -race -count=1` | Passed. Includes existing invalid-blob, missing-gate, response-cleanup, and serve-error coverage. |
| `make test` | Passed in full with race detection. |
| `make lint` | Passed: go vet and golangci-lint, zero issues. Initial wrapcheck findings were fixed. |
| LSP diagnostics | No diagnostics for telegram.go or serve.go. Docker-tagged test navigation is unavailable in the host LSP; Docker build runs vet and compiles those tests. |
| `go fix -diff ./...` | Exit 1 with modernization proposals; no changes applied. |
| `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` | Passed. |
| `docker run --rm --network none azath-docker-e2e` | Passed, including waiting approval, hostile callbacks, denial, cache reuse, restart, corrupt blobs, and disabled-device prompt suppression. |
| `git diff --check` | Passed. |

Final logs: `/tmp/azath-task1-{focused,repeat,make-test,lint,modernize,docker-build,docker-run}.log`. Live DSM and systemd compatibility remain unverified. Tasks 2–4 are not implemented by this bounded task.

Skills used for Task 1: `skill-loader` selected `codebase-research`, `go-best-practices`, and `write`. Research traced gate state, callbacks, configuration, callers, and Docker fixtures. Go guidance supplied explicit ownership, bounded concurrency, and race validation. Writing guidance kept this evidence separate from whole-plan acceptance.

### Skills loaded and used in this execution
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | prompt-required | Code implementation | Selected Go, CLI, shell, documentation, and research guidance. |
| codebase-research | user-requested | Cross-package behavior | Traced server, client, gate, startup, tests, and pinned codec implementation before edits. |
| go-best-practices | user-requested | Go changes | Explicit buffer ownership, fail-closed behavior, focused tests, race checks, and minimal changes. |
| cli-best-practices | user-requested | CLI runtime | Preserved errors after server cleanup and avoided an extra plaintext allocation. |
| script-best-practices | user-requested | Required shell verification | Preserved POSIX scripts; ran syntax, ShellCheck, and installer tests. |
| diataxis | user-requested | Execution documentation | Separated implementation evidence from requirements and acceptance claims. |
| write | user-requested | Plan evidence | Recorded concrete results, limits, and incomplete work without claiming full completion. |

### Task 2 completion evidence
Task 2 is complete in this worktree. Task 1 safety and blocking-gate changes remain in place. Task 3 is not implemented: the active standalone CLI client schema, generic client command, and boot helper remain unchanged by Task 2. No commit, push, PR, deployment, live service, or credential access was performed.

- Test first: `TestRejectRegistryFields` failed because empty and path-bearing registry mappings were accepted. The startup test also failed before removal with a pre-existing device state file and a config-only name change. Both pass after removal.
- `TestConfigRenamePreservesExistingBlob` seals with the old configured name, constructs a fresh server with the same UUID and a new name, and unseals the existing blob. Its gate checks that approval uses the new configured name. No registry is constructed.
- Removed the persistent registry package, registry YAML field and validation, startup HKDF/HMAC derivation and reconciliation, server registry argument and registration/lookup, notifier interface/options/background work, server gate compatibility aliases, unused config Diff, and inactive nested client schema.
- Semantic references showed that `DeriveKey` had only the registry startup caller plus its own tests; removed it and those API-specific tests. `Loaded` has a real readiness consumer and remains. Sealer/key lifecycle and concurrency coverage remain. Telegram callback HMAC is independent and remains.
- Migrated server and CLI integration fixtures, config-command fixtures, Docker server config, examples, deployment runbook, architecture reference, and AGENTS.md. Registry-specific Seal coverage tests configured-device operation without persistent state. Auth, invalid-blob, disabled-device, failure-response, token-zeroing, panic, and blocking-approval coverage remain.
- Removed registry size/load-error and notifier failure metrics, the two corresponding alerts, and their obsolete expectations. Request, gate, key, latency, and gate API error metrics remain. Prometheus's metrics registry is unrelated to device persistence and remains.

| Command/check | Result |
|---|---|
| `go test ./internal/config ./internal/server ./cmd/azath -race -count=1` | Passed. |
| `make test` | Passed in full with race detection. |
| `make lint` | Passed: go vet and golangci-lint, zero issues. Initial test-file path warnings were resolved with scoped annotations for test-owned temporary files. |
| `go test ./alerts -v -count=1` | Passed all alert checks. |
| `docker run --rm --network none --entrypoint promtool -v "$PWD/alerts:/alerts:ro" prom/prometheus:v3.5.0 check rules /alerts/azath.yml` | Passed: four valid rules. |
| `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` | Passed. |
| `docker run --rm --network none azath-docker-e2e` | Passed: TLS seal, original waiting approval, hostile callbacks, denial, cache reuse, corrupt blob, restart with persistent key/config, disabled-device suppression. |
| LSP diagnostics | No diagnostics for server.go, serve.go, config.go, manager.go, metrics.go, config_authority_test.go, or serve_test.go. |
| `go fix -diff ./...` | Exit 1 with modernization proposals; no changes applied. |
| `git diff --check` | Passed. |
| Residual search | No production device registry, notifier, compatibility aliases, DeriveKey, obsolete metric names, or inactive ClientConfig types remain. Registry strings are rejection/stale-state tests, historical plans, or Prometheus terminology. |

Logs: `/tmp/azath-task2-{red-startup,focused-final,make-test,lint,alerts,promtool,modernize,docker-build,docker-run}.log`. The registry rejection failure was captured in tool output. No live DSM or systemd compatibility is claimed. Task 4's whole-design documentation and acceptance review remain separate from this completed Task 2 migration.

#### Skills loaded and used for Task 2
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | user-requested | Cross-package implementation | Selected Go, CLI, research, and documentation guidance. |
| feature-worktree | agent-selected | Existing feature worktree | Kept all changes in the requested worktree. |
| codebase-research | skill-loader | Runtime and test consumers | Traced config, startup, server, metrics, and fixtures. |
| go-best-practices | skill-loader | Go removal and tests | Preserved ownership and safety coverage; ran race tests and lint. |
| cli-best-practices | skill-loader | Config and startup contract | Rejected obsolete fields and preserved the active client CLI. |
| diataxis | skill-loader | Reference documentation | Removed unsupported schema and state claims. |
| write | skill-loader | Plan and docs | Recorded exact checks and bounded completion. |

### Task 3/4 completion evidence
All seven requirements are implemented and verified within the local test scope. Earlier stage descriptions above are historical checkpoints, not outstanding work.

- Preserved the previous worker's implementation and inspected orphan PID 69789 before starting Docker. That process had exited. `/tmp/azath-task3-docker-build.log` showed success; its run log showed failure at the approval fixture. A denied multi-share invocation had started another pending approval, and the next invocation used that stale callback. The fixture now uses one share for denial and explicitly waits for a fresh prompt before the multi-share approval.
- The shared client schema in `seal.go` validates canonical identity, endpoints, share names, duplicate names, path lengths/NUL, YAML fields/documents, and finite input limits before mounting. Seal retains its fixed 30-second RPC deadline. No timeout flag is required for a finite RPC lifetime.
- The dedicated Synology command waits for each original RPC, transfers decoded plaintext ownership, zeros it after use, and mounts sequentially through the fixed DSM executable. Transport errors/empty responses permit failover; nonempty responses do not. Failed mounts do not retry or stop later shares. Invalid passphrases, cancellation, aggregate failure, and safe output have focused tests. Mount contexts have a one-minute deadline; RPC timeouts are bounded to 30 minutes per endpoint.
- The boot helper invokes the command once, with no generic runner or mount retry loop. Its shipped-script tests verify this contract.
- Semantic references for `gate.Pending` showed only its declaration, server branch, and obsolete server test. Removed all three and obsolete metric label documentation; no compatibility result remains. Internal pending approval requests remain necessary and are not an RPC result.
- Telegram HTTP timeout is 40 seconds, exceeding its 30-second long-poll parameter by 10 seconds. JSON decoder reads are bounded to 64 KiB for send responses and 1 MiB for update responses. An oversized-response regression fails closed and checks the timeout margin.
- Existing zero-cache, cache isolation/expiry, cancellation, prompt rate-limit, resource-admission, callback replacement, API error, response-codec cleanup, gRPC serialization, disabled-device, corrupt-input suppression, and serve-error tests remain intact and pass.
- Docker uses the actual dedicated command with two shares and asserts exactly one approval prompt after the separate denial scenario. The cached four-share invocation records exactly `homes, media, fail, backup`, proving partial failure does not skip later shares or retry a mount. Corrupt blobs produce no prompt. Hostile callbacks, waiting approval, restart, and disabled-device suppression remain covered.
- Reviewed README, AGENTS, SECURITY, installation guides, deployment guide, architecture, runbooks, examples, helper, Docker documentation, metrics, alerts, and CI references. Removed the remaining stale command in SECURITY and corrected deployment failure semantics and endpoint ordering. No production generic-client, registry/notifier state, Pending result, or pending-retry contract remains. Remaining pending terms describe real in-flight approvals or unverified hardware; registry terms in rejection tests and Prometheus are intentional.

| Final command/check | Exact result |
|---|---|
| `make test` | Exit 0; full suite with `-race -count=1`. |
| `make vet` | Exit 0. |
| `make lint` | Exit 0; go vet and golangci-lint, `0 issues.` |
| `go test ./internal/gate -race -count=5` | Exit 0, including response bounds and cache/rate/cancellation cases. |
| `sh -n deploy/scripts/install.sh deploy/scripts/install_test.sh deploy/synology/azath-unseal.sh` | Exit 0. |
| `shellcheck --shell=sh deploy/scripts/install.sh deploy/scripts/install_test.sh deploy/synology/azath-unseal.sh` | Exit 0. |
| `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` | Exit 0 after final code/test edits. |
| `docker run --rm --network none azath-docker-e2e` | Exit 0; TestDockerUnlock passed in 8.75 seconds. |
| `docker run --rm -e AZATH_INSTALL_TEST_CONTAINER=1 -v "$PWD:/src:ro" -w /src debian:13-slim dash deploy/scripts/install_test.sh` | Exit 0; 42 passed, 0 failed. |
| `docker run --rm -e AZATH_INSTALL_TEST_CONTAINER=1 -v "$PWD:/src:ro" -w /src busybox:1.37 sh deploy/scripts/install_test.sh` | Exit 0; 42 passed, 0 failed. |
| `go fix -diff ./...` | Exit 1 with modernization proposals; inspected, no changes applied. This is advisory, not a test failure; unrelated modernization remains out of scope. |
| LSP diagnostics | No diagnostics for telegram.go, seal.go, client.go, or server.go. Docker-tagged LSP package metadata is unavailable; Docker build compiles/vets these tests. |
| `git diff --check` | Exit 0. |

Final evidence logs: `/tmp/azath-final-{test,vet,lint,gate-repeat,shell,installer-debian,installer-busybox,modernize,docker-build,docker-run}.log`, with corresponding `.exit` files. Residual-reference search: `/tmp/azath-final-residuals.log`.

Acceptance review: R1 original approved RPC and failed-input suppression; R2 current zero-cache waiters and device-scoped reuse; R3 bounded prompts/work and callback identity; R4 config-only startup/rename and obsolete-field rejection; R5 sequential fixed-command multi-share operation and safe partial failure; R6 error propagation, indistinguishable failure lengths, and explicit response cleanup; R7 supported docs/schema/commands/metrics only. All pass local acceptance. The eight user-specified checks (shared schema/Seal deadline, invalid inputs, cancellation, cache zero/rate limits, cleanup, Pending removal, HTTP timeout margin, bounded JSON) are covered by inspection and focused/full tests.

Limits: no real DSM/NAS, Telegram, 1Password, systemd host, release publication, or cold-boot validation. Container success does not certify DSM compatibility. Runtime/transport copies and DSM argv strings cannot be reliably zeroed. No unresolved implementation blocker remains.

#### Skills loaded and used for Task 3/4 continuation
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | user-requested | Continued implementation | Routed matching guidance before edits. |
| codebase-research | skill-loader | Existing cross-package changes | Traced client, gate, codec, fixtures, docs, and orphan logs. |
| go-best-practices | skill-loader | Go safety changes | Bounded IO, ownership, focused regressions, race tests, diagnostics. |
| cli-best-practices | skill-loader | Dedicated command | Validated inputs and preserved safe partial results; no unrelated output redesign. |
| script-best-practices | skill-loader | Boot/installer checks | Verified shipped POSIX scripts with syntax, ShellCheck, and both installer suites. |
| diataxis | skill-loader | Installation/reference docs | Kept operation instructions and architecture contracts accurate. |
| write | skill-loader | Docs and evidence | Recorded exact results and explicit live-platform limits. |

### Post-implementation simplify pass
Reviewed the feature diff against `origin/main` in the existing dirty `maruina/device-unlock` worktree. Status before edits is saved in `/tmp/azath-simplify-status-before.log`. Removed only the redundant UUID parse in `runSeal`: `loadClientConfig` already rejects invalid or noncanonical UUIDs before returning. The loader remains the validation boundary; error ordering and reachable errors are unchanged. Existing invalid-UUID and successful Seal tests cover this path. No tests were changed.

Left approval synchronization, callback binding, limits, error sanitization, metrics, response cleanup, duplicate decryption around approval, endpoint failover, mount sequencing, and boot helper unchanged. Modernization-only changes were rejected. No commit, push, or PR operation was performed; the simplification remains uncommitted with the approved implementation.

| Check | Result |
|---|---|
| `go test ./cmd/azath -run 'TestSealCmd_|TestSharedClientConfigRejectsInvalidInputs|TestSynologyInvalidConfigBeforeMounts' -race -count=1` | Passed before and after the edit. |
| `make test` | Passed after the edit with race detection. The supplied pre-pass baseline also passed. |
| `make lint` | Passed after the edit, zero issues. The supplied pre-pass baseline also passed. |
| `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` | Passed after the edit. |
| `docker run --rm --network none azath-docker-e2e` | Passed after the edit. |
| LSP navigation and diagnostics | Traced `runSeal` references and loader context before editing; no diagnostics for `seal.go` after editing. |
| `go fix -diff ./...` | Exit 1 with advisory modernization proposals in secret/gate code; no changes applied. |
| `git diff --check` | Passed. |

Logs: `/tmp/azath-simplify-{focused-before,focused-after,test,lint,docker-build,docker-run,modernize}.log`. These are implementation-side checks, not independent final verification. A fresh `/verify` must use this plan. Live DSM, cold boot, systemd, real services, and reliable zeroing of runtime/transport/argv copies remain outside this pass.

#### Skills loaded and used for simplification
| Skill | Source | Why loaded | How used |
|---|---|---|---|
| skill-loader | prompt-required | Simplify workflow | Selected matching language and domain guidance. |
| resolve-worktree | prompt-required | Explicit worktree target | Resolved the existing absolute worktree path. |
| feature-worktree | prompt-required | File edits | Continued on the feature branch without moving the base checkout. |
| codebase-research | skill-loader | Unfamiliar changed flow | Traced CLI, config, gate, server, ownership, and existing tests before proposing removal. |
| go-best-practices | skill-loader | Go diff | Removed duplicate validation at the established boundary; used semantic navigation, race tests, and diagnostics. |
| cli-best-practices | skill-loader | Seal command | Preserved reachable input errors and RPC behavior; did not redesign output or flags. |
| script-best-practices | skill-loader | Changed boot helper | Reviewed the single-exec launcher and left its behavior intact. |
| diataxis | skill-loader | Documentation diff | Kept operational contracts and test evidence separate; made no user-guide restructuring. |
| write | skill-loader | Plan ledger and handoff | Recorded bounded changes, exact checks, and unverified limits. |
