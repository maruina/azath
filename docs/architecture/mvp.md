# MVP architecture

azath is a homelab KMS that protects Talos Linux disk encryption keys and Synology encrypted shared-folder passphrases behind a Telegram approval gate. The MVP can use one LAN server or a redundant LAN + VPS pair sharing a master key and device configuration. Caddy terminates TLS in front of loopback-only gRPC listeners. A server must not depend on the encrypted shared folders it unlocks for its storage, networking, or DNS.

## Threat model

**What azath protects:**
- Stolen Talos or Synology hosts cannot auto-unseal without Telegram approval.
- With multiple endpoints configured, `azath synology` can try a second server after a connection or RPC failure. The second server still requires its own Telegram approval or approval-cache hit.
- Disabling a device in config and deploying to every server stops sealing and unsealing.

**What azath does not protect against:**
- Compromise of both a sealed blob and the azath master key: anyone with the sealed blob and the master key can decrypt directly.
- Approving a malicious request: an operator who approves a Telegram prompt without verifying the server name, device name, and UUID.
- A stolen Telegram session: if an attacker can approve Telegram callbacks, they can unseal any device (within the approval cache TTL).

## Shared-state requirements
These requirements apply when deploying more than one server. Approval caches remain independent and in-memory on each server.

- **Master key**: LAN and VPS must use the same raw 32-byte master key file. Both servers derive the same AES-256-GCM keys and instance tags from it. A client can seal through one server and unseal through the other.
- **Device config**: LAN and VPS must use the same `devices[]` list in their server configs. Adding or disabling a device requires updating both configs.
- **Telegram bot tokens**: LAN and VPS must use separate bot tokens when using long polling. Telegram `getUpdates` is a single queue per bot token; if both servers poll the same bot, one server will consume callbacks intended for the other. Both bots may send messages to the same chat/user.
- **Device authority**: `devices[]` in the server config is the sole device authority. Renaming a device preserves access to existing blobs for its UUID. Startup and Seal do not create device state files.

## Server model

- `azath serve` binds gRPC to loopback only (e.g. `127.0.0.1:7800`).
- Caddy or another TLS proxy terminates TLS and forwards h2c gRPC to azath on loopback.
- Metrics listener also binds to loopback (`127.0.0.1:9090`) with no authentication.
- Master key source is a local file (`master_key.source: file`).
- Gate type is Telegram only (`gate.type: telegram`).
- No Tailscale gate, no YubiKey, no 1Password master key source in MVP.

## Approval and key lifetime
Unseal validates the blob before prompting, clears the validation plaintext, waits for approval, and decrypts again only after approval. Concurrent requests for one UUID share a prompt. Approval releases current waiters even with cache TTL zero; positive cache TTL authorizes later requests for that UUID until expiry. Denial, timeout, cancellation, API error, and shutdown fail closed. Prompt rate limits include unsuccessful attempts.

The gate admits at most 64 pending devices, 32 waiters per device, and 8 HTTP sends. Cache and prompt-history maps each hold at most 1024 devices. HTTP requests time out after 40 seconds, beyond Telegram's 30-second long poll; JSON reads are limited to 64 KiB for sends and 1 MiB for updates.

The server codec clears application-owned response bytes after protobuf serialization. Clients own and clear decoded plaintext after each mount. Transport/runtime copies, DSM argument strings, and the AES expanded key schedule cannot be reliably cleared by this code.

## Client model

- `azath seal` is a bootstrap CLI used once per secret to create sealed blobs. It reads the plaintext interactively (`--prompt`) or from stdin and calls the KMS `Seal` RPC for the configured device UUID.
- `azath synology` unlocks configured shares sequentially at boot using only the fixed DSM executable. It continues after share failure and returns nonzero on partial failure.
- `azath synology` tries endpoints in order after an RPC failure or empty response. A nonempty response ends endpoint failover for that share. A failed mount does not stop later shares. Denied, wrong-key, and decrypt-error responses contain random bytes, so a reachable failing server can prevent use of a later endpoint.
- `azath seal` uses the first selected endpoint with a 30-second RPC deadline; it does not retry across endpoints.
- Synology Unseal has a per-endpoint deadline of 10 minutes by default (`--timeout`, maximum 30 minutes). Each mount has a one-minute deadline. Caller cancellation stops later mounts.

## Ports

| Port | Service | Interface | Notes |
|------|---------|-----------|-------|
| 7800 | gRPC (h2c) | loopback | Caddy proxies external 443 → loopback 7800 |
| 9090 | Metrics, health | loopback | Prometheus scrape, `/healthz`, `/readyz` |
| 443 | gRPC (TLS) | external | Caddy listener, forwarded as h2c to 7800 |
