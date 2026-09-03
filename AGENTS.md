# azath

Homelab KMS for Talos Linux disk encryption and Synology encrypted volume unsealing. Implements the Talos KMS gRPC protocol with AES-256-GCM, Telegram approval, and config-driven device disablement.

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
- Registry/wipe semantics are not part of the MVP.
- azath serves plaintext h2c gRPC on loopback only; Caddy terminates TLS externally.
- Synology client runs one action per invocation. Call `azath client` multiple times for multiple shares.

## Module

`github.com/maruina/azath` — Go 1.26. Entry point: `cmd/azath/main.go`.
Subcommands: `serve`, `client`, `seal`, `config validate`, `config new-device`.
`client` and `seal` are stubs until Tasks 11 and 12.

## Directory structure

```text
alerts/          # Prometheus alert rules + tests
cmd/azath/       # cobra entry point + subcommands
internal/
  config/        # Load, LoadFromBytes, Validate, Diff, SafeAttrs
  crypto/        # Sealer, Zero, ZeroOnReturn
  fsutil/        # atomic file write
  gate/          # Telegram approval gate
  keymanager/    # master key lifecycle + HKDF derivation
  observability/ # slog, Prometheus, health HTTP
  registry/      # legacy device store; avoid adding MVP behavior here
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
- Run `make test` before marking work complete.

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
- Do not log or return key material, sealed blobs, passphrases, bot tokens, or seal tokens in errors.
- Do not convert key/passphrase bytes to strings except at unavoidable external API/exec boundaries; document any exception.
- Use constant-time comparison for bearer tokens.
- Known limitation: Go's AES expanded key schedule cannot be zeroed.

## Server invariants

- `Seal` requires a bearer token and only accepts configured, enabled devices.
- `Unseal` returns `codes.OK` plus random bytes for every failure path.
- Disabled, unknown, gate-denied, decrypt-error, wrong-instance, and panic paths must be indistinguishable to callers.
- Disabled devices must not trigger Telegram prompts or use approval cache.
- `NewGRPCServer` must keep `grpc.MaxRecvMsgSize(1 << 20)` and panic recovery.
- `Close()` drains in-flight work before zeroing secrets.

## Telegram gate requirements

- Gate zero value is denied.
- Wrong user, wrong chat, malformed callback data, stale callback data, and tampered callback data must not approve.
- One pending approval per device UUID.
- Pending entries must be deleted on approve, deny, timeout, context cancellation, and API error.
- Approval cache is in-memory only and disabled when TTL is zero.
- Telegram API failures fail closed.

## Deploy

- `deploy/` contains static production examples; no Go build or test references them.
- Installer validation: run `docker run --rm -e AZATH_INSTALL_TEST_CONTAINER=1 -v "$PWD:/src:ro" -w /src debian:13-slim dash deploy/scripts/install_test.sh`, then the equivalent `busybox:1.37` command with `sh`; run `sh -n` and `shellcheck --shell=sh` for both installer scripts.
- Release validation: run `goreleaser check` and `goreleaser release --snapshot --clean`; assert stable archives contain only `azath` and `deploy/systemd/azath.service` and that checksums name both archives.
- Before public rollout, audit history with Gitleaks, verify public assets through unauthenticated latest and pinned URLs, smoke-test Debian 13 and DSM 7, and upgrade redundant Debian servers one at a time with `/readyz` between them.
- `systemd-analyze verify` and `caddy validate` validate systemd/Caddy files when tools are available.

## Client CLI MVP

Use a tiny client config for identity/endpoints:

```yaml
device:
  name: synology
  uuid: 00000000-0000-0000-0000-000000000000
endpoints:
  - https://azath-lan.example:443
  - https://azath-vps.example:443
```

Run one command per sealed blob/share:

```bash
azath client --config /etc/azath/client.yaml \
  --sealed-blob /etc/azath/secrets/homes.key.sealed \
  -- /usr/syno/sbin/synoshare --enc_mount homes
```

The client appends the unsealed plaintext as the final exec argument. It must never use a shell or log command arguments.
