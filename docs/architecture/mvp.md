# MVP architecture

azath is a homelab KMS that protects Talos Linux disk encryption keys and Synology encrypted volume passphrases behind a Telegram approval gate. The MVP deploys two servers (LAN + VPS) sharing a master key and device configuration, with Caddy terminating TLS in front of loopback-only gRPC listeners.

## Threat model

**What azath protects:**
- Stolen Talos or Synology hosts cannot auto-unseal without Telegram approval.
- A single-server azath outage does not block unseals — the client fails over to the second server.
- Disabling a device in config and deploying to both servers stops sealing and unsealing without modifying the registry.

**What azath does not protect against:**
- Compromise of both a sealed blob and the azath master key: anyone with the sealed blob and the master key can decrypt directly.
- Approving a malicious request: an operator who approves a Telegram prompt without verifying the server name, device name, and UUID.
- A stolen Telegram session: if an attacker can approve Telegram callbacks, they can unseal any device (within the approval cache TTL).

## Shared-state requirements

- **Master key**: LAN and VPS must use the same raw 32-byte master key file. Both servers derive the same AES-256-GCM keys, HMAC keys, and instance tags from it. A client can seal through one server and unseal through the other.
- **Device config**: LAN and VPS must use the same `devices[]` list in their server configs. Adding or disabling a device requires updating both configs.
- **Telegram bot tokens**: LAN and VPS must use separate bot tokens when using long polling. Telegram `getUpdates` is a single queue per bot token; if both servers poll the same bot, one server will consume callbacks intended for the other. Both bots may send messages to the same chat/user.
- **Registry state**: Registry state (wiped flags) is per-server and is **not** the MVP theft-response mechanism. Theft response is handled by setting `devices[].disabled: true` in config and deploying it to both servers.

## Server model

- `azath serve` binds gRPC to loopback only (e.g. `127.0.0.1:7800`).
- Caddy or another TLS proxy terminates TLS and forwards h2c gRPC to azath on loopback.
- Metrics listener also binds to loopback (`127.0.0.1:9090`) with no authentication.
- Master key source is a local file (`master_key.source: file`).
- Gate type is Telegram only (`gate.type: telegram`).
- No Tailscale gate, no YubiKey, no 1Password master key source in MVP.

## Client model

- `azath seal` is a bootstrap CLI used once per device to create sealed blobs. It requires a seal token and reads the plaintext interactively (`--prompt`) or from stdin.
- `azath client` is a one-shot CLI used at every boot to unseal one encrypted volume. It appends the plaintext passphrase as the final argument to a user-provided command.
- Clients try endpoints in order and fall back on failure.

## Ports

| Port | Service | Interface | Notes |
|------|---------|-----------|-------|
| 7800 | gRPC (h2c) | loopback | Caddy proxies external 443 → loopback 7800 |
| 9090 | Metrics, health | loopback | Prometheus scrape, `/healthz`, `/readyz` |
| 443 | gRPC (TLS) | external | Caddy listener, forwarded as h2c to 7800 |
