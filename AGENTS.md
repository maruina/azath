# azath

Homelab KMS for Talos Linux disk encryption and Synology encrypted shared-folder unsealing. Implements the Talos KMS gRPC protocol with AES-256-GCM, Telegram approval, and config-driven device disablement.

This file is the agent reference for working in the repo. The project overview, install links, contribution, and release flow live in `README.md`; deployment, install, and architecture guides live in `docs/`.

## Commands

```bash
make build   # build bin/azath (version + commit injected via ldflags)
make test    # go test ./... -v -race -count=1
make lint    # go vet + golangci-lint
make vet     # go vet only
make clean   # remove bin/
```

## MVP scope

- Master key source: local raw 32-byte file only.
- Gate: Telegram only. Every successful Unseal must pass Telegram approval or approval cache.
- Device source of truth: `devices[]` in server config.
- Theft response: set `devices[].disabled: true` and deploy the config to every server.
- Config is the sole device authority; no persistent registry or wipe state.
- azath serves plaintext h2c gRPC on loopback only; Caddy terminates TLS externally.
- One LAN server is sufficient; LAN + VPS is optional redundancy. The server's storage, networking, and DNS must not depend on a shared folder it unlocks.
- Synology unlocks all configured shares in one sequential invocation.

## Module

`github.com/maruina/azath` — Go 1.26. Entry point: `cmd/azath/main.go`.
Subcommands: `serve`, `synology`, `seal`, `config validate`, `config new-device`,
`config enable-device`, `config disable-device`.

## Directory structure

```text
alerts/          # Prometheus alert rules + tests
cmd/azath/       # cobra entry point + subcommands
internal/
  config/        # Load, LoadFromBytes, Validate, SafeAttrs
  crypto/        # Sealer, Zero, ZeroOnReturn
  fsutil/        # atomic file write
  gate/          # Telegram approval gate
  keymanager/    # master key lifecycle and sealer creation
  observability/ # slog, Prometheus, health HTTP
  secret/        # op:// resolution
  server/        # KMS gRPC server
  testutil/      # shared test helpers
```

## Code conventions

- Errors: `fmt.Errorf("context: %w", err)`, lowercase, no trailing punctuation.
- Stubs: `fmt.Errorf("<cmd>: not yet implemented")`.
- Prefer `strings.CutPrefix` over `HasPrefix` + `TrimPrefix`.
- Prefer `cmp.Compare` in sort comparators.
- Sets use `map[K]struct{}`.
- No testify.
- Table tests use a `mutate func(*T)` field instead of switching on test names.
- Shared test helpers go in `internal/testutil/`.

## Testing conventions

- `t.Parallel()` on top-level tests and subtests unless using `t.Setenv`.
- Use `t.Context()` in tests.
- Use `httptest.NewRequestWithContext`.
- Do not call `t.Fatalf` from goroutines; use `t.Errorf` plus return or collect errors in a channel.
- Run `make test` before marking work complete; it does not run Docker E2E.
- For changes to the Telegram gate, client/server wiring, endpoints, or Synology boot helper, also run `docker build -f tests/docker/Dockerfile -t azath-docker-e2e .` and `docker run --rm --network none azath-docker-e2e`. CI runs the same suite in the `Docker E2E` job. Coverage and limits are in `tests/docker/README.md`.
- Keep Docker tests isolated: disposable secrets, no real Telegram/1Password/NAS access, bounded waits, and process cleanup. Container success does not establish DSM or systemd compatibility.

## Config rules

- `server.listen` and `server.metrics_listen` must be explicit loopback addresses, e.g. `127.0.0.1:7800` and `127.0.0.1:9090`.
- `server.name` is required with Telegram gate.
- `master_key.source` must be `file`.
- `gate.type` must be `telegram`.
- `devices[].name` and canonical UUIDs must be unique.
- `devices[].disabled: true` denies Seal and Unseal before Telegram.
- An empty `devices: []` is valid as a `config new-device` bootstrap stub; `config new-device` appends, it does not synthesize a complete config.
- `*_ref` fields must be `op://<vault>/<item>/<field>`.

## Crypto and key handling

- Blob format: `[4-byte instance tag | 12-byte nonce | ciphertext + GCM tag]`.
- Nonce = 4-byte random prefix + 8-byte atomic counter seeded at construction.
- Every plaintext key/passphrase/master-key slice must be zeroed immediately after use with `crypto.Zero` or `crypto.ZeroOnReturn`.
- Do not log or return key material, sealed blobs, passphrases, or bot tokens in errors.
- Do not convert key/passphrase bytes to strings except at unavoidable external API/exec boundaries; document any exception.
- Known limitation: Go's AES expanded key schedule cannot be zeroed.

## Server invariants

- `Seal` only accepts configured, enabled devices in `devices[]`.
- `Unseal` returns `codes.OK` plus random bytes for every failure path.
- Disabled, unknown, gate-denied, decrypt-error, wrong-instance, and panic paths must be indistinguishable to callers.
- Disabled devices must not trigger Telegram prompts or use approval cache.
- `NewGRPCServer` must keep `grpc.MaxRecvMsgSize(1 << 20)` and panic recovery.
- Drain RPCs with gRPC graceful shutdown before shutdown. Gate close drains its own work before zeroing secrets.

## Telegram gate requirements

- Gate zero value is denied.
- Wrong user, wrong chat, malformed callback data, stale callback data, and tampered callback data must not approve.
- One pending approval per device UUID.
- Telegram button `callback_data` must be 1–64 bytes. Test stubs must enforce this limit.
- Approval callbacks must be bound to the original pending request; an old callback must not approve or remove a replacement request.
- `serve --telegram-api-base` requires `--dev`. Use only disposable tokens with test endpoints; never use the override in production.
- Pending entries must be deleted on approve, deny, timeout, context cancellation, and API error.
- Check waits for approval or denial. There is no Pending result or retry protocol.
- Approval cache is in-memory only and disabled when TTL is zero; current waiters still complete on approval.
- HTTP timeout must exceed the 30-second Telegram long poll. Bound JSON response reads (64 KiB send, 1 MiB updates).
- Telegram API failures fail closed.

## Deploy

- `deploy/` contains deployment examples and installer scripts. `cmd/azath/synology_script_test.go` reads the shipped Synology helper to test per-share failure handling; preserve that coverage when changing the script.
- Installer validation: run `docker run --rm -e AZATH_INSTALL_TEST_CONTAINER=1 -v "$PWD:/src:ro" -w /src debian:13-slim dash deploy/scripts/install_test.sh`, then the equivalent `busybox:1.37` command with `sh`; run `sh -n` and `shellcheck --shell=sh` for both installer scripts.
- Release validation: run `goreleaser check` and `goreleaser release --snapshot --clean`; assert stable archives contain only `azath` and `deploy/systemd/azath.service` and that checksums name both archives.
- Before public rollout, audit history with Gitleaks, verify public assets through unauthenticated latest and pinned URLs, smoke-test Debian 13 and DSM 7, and upgrade redundant Debian servers one at a time with `/readyz` between them.
- `systemd-analyze verify` and `caddy validate` validate systemd/Caddy files when tools are available.

## Synology CLI
Use the same config for `seal` and `synology`: `device` identity, `endpoints`, and `shares` entries with `name` and `sealed_blob`. See `examples/client-synology.yaml` and `docs/runbooks/synology.md`.

Run `azath synology --config /etc/azath/client-synology.yaml`. It calls only `/usr/syno/sbin/synoshare --enc_mount NAME PASSPHRASE`, directly, once per share. Continue after failure and return aggregate nonzero. Never expose subprocess diagnostics or plaintext. Only transport errors or empty responses permit endpoint failover; never retry a mount across endpoints. Cancellation stops further mounts. Unseal defaults to a 10-minute per-endpoint deadline (maximum 30 minutes); mounts have a one-minute deadline. Seal has a fixed 30-second RPC deadline. Use a narrow internal mount function seam in unit tests and the fixed DSM path in Docker. No generic runner or compatibility command is supported.
