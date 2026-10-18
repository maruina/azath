# Runtime behavior
## Server
- `azath serve` binds gRPC to loopback only (e.g. `127.0.0.1:7800`).
- Caddy or another TLS proxy terminates TLS and forwards h2c gRPC to azath on loopback.
- Metrics listener also binds to loopback (`127.0.0.1:9090`) with no authentication.
- Master key source is a local file (`master_key.source: file`).
- Gate type is Telegram only (`gate.type: telegram`).
- Device UUIDs are authorization lookups, not client authentication. `Seal` has no Telegram or bearer-token check; configured, enabled UUIDs can seal. `Unseal` additionally requires a valid blob and Telegram approval or an approval-cache hit.
- No Tailscale gate, no YubiKey, no 1Password master key source in MVP.

## Approval and key lifetime
Unseal validates the blob before prompting, clears the validation plaintext, waits for approval, and decrypts again only after approval. Concurrent requests for one UUID share a prompt. Approval releases current waiters; the approval cache authorizes later requests for that UUID until expiry. `gate.telegram.approval_cache_ttl` defaults to `2m` and must be positive when set. The internal gate supports zero TTL in tests, but server config validation rejects `0s`. Denial, timeout, cancellation, API error, and shutdown fail closed. Prompt rate limits include unsuccessful attempts.

The gate admits at most 64 pending devices, 32 waiters per device, and 8 HTTP sends. Cache and prompt-history maps each hold at most 1024 devices. HTTP requests time out after 40 seconds, beyond Telegram's 30-second long poll; JSON reads are limited to 64 KiB for sends and 1 MiB for updates.

The server codec clears application-owned response bytes after protobuf serialization. Clients own and clear decoded plaintext after each mount. Transport/runtime copies, DSM argument strings, and the AES expanded key schedule cannot be reliably cleared by this code.

The device list is loaded at startup. Config edits, including disablement, require a process restart on every server; there is no config watcher or reload command. The gRPC receive-message limit is 1 MiB, including protobuf overhead.

On Linux, production startup attempts memory locking and disables core dumps. Failure logs a warning but does not prevent startup. Development mode skips these operations. `/readyz` checks that the master key is loaded; it does not test Telegram, 1Password token validity after startup, or client access through Caddy.

## Clients
- `azath seal` is a bootstrap CLI used once per secret to create sealed blobs. It reads the plaintext interactively (`--prompt`) or from stdin and calls the KMS `Seal` RPC for the configured device UUID.
- `azath synology` unlocks configured shares sequentially at boot using only the fixed DSM executable. It continues after share failure and returns nonzero on partial failure.
- `azath synology` tries endpoints in order after an RPC failure or empty response. A nonempty response ends endpoint failover for that share. A failed mount does not stop later shares. Denied, wrong-key, and decrypt-error responses contain random bytes, so a reachable failing server can prevent use of a later endpoint.
- `azath seal` uses the first selected endpoint with a 30-second RPC deadline; it does not retry across endpoints.
- Synology Unseal has a per-endpoint deadline of 10 minutes by default (`--timeout`, maximum 30 minutes). Each mount has a one-minute deadline. Caller cancellation stops later mounts.

Production CLI connections use system certificate verification and require TLS 1.3 or later. Endpoints accept `https://host:port` or bare `host:port`, including bracketed IPv6 addresses. Paths, credentials, queries, and fragments are rejected. `--insecure-dev` selects plaintext transport for local tests.

## Example deployment ports
| Port | Service | Interface | Notes |
|------|---------|-----------|-------|
| 7800 | gRPC (h2c) | loopback | Caddy proxies external 443 → loopback 7800 |
| 9090 | Metrics, health | loopback | Prometheus scrape, `/healthz`, `/readyz` |
| 443 | gRPC (TLS) | external | Caddy listener, forwarded as h2c to 7800 |

For design context, see [Architecture and threat model](../architecture/mvp.md). For deployment steps, see [Deploy LAN + VPS servers](../runbooks/deploy-lan-vps.md).
