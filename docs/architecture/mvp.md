# Architecture and threat model
azath is a homelab KMS that protects Talos Linux disk encryption keys and Synology encrypted shared-folder passphrases behind a Telegram approval gate. The MVP can use one LAN server or a redundant LAN + VPS pair sharing a master key and device configuration. Caddy terminates TLS in front of loopback-only gRPC listeners. A server must not depend on the encrypted shared folders it unlocks for its storage, networking, or DNS.

## Threat model
**What azath protects:**
- Successful unsealing requires Telegram approval or an unexpired, device-scoped approval-cache entry, including requests from stolen clients.
- With multiple endpoints configured, `azath synology` can try a second server after a connection or RPC failure. The second server still requires its own Telegram approval or approval-cache hit.
- Disabling a device in config and deploying the config and restarting every server stops sealing and unsealing.

**What azath does not protect against:**
- Compromise of both a sealed blob and the azath master key: anyone with the sealed blob and the master key can decrypt directly.
- Approving a malicious request: an operator who approves a Telegram prompt without verifying the server name, device name, and UUID.
- A stolen authorized Telegram session: an attacker who can approve prompts can authorize requests for configured, enabled devices and valid blobs.
- Device impersonation: the request UUID is not a client credential. The server has no bearer-token or mutual-TLS client authentication; it relies on Telegram approval for unsealing. An approval-cache entry also authorizes other requests for the same UUID while it remains valid.

## Shared-state requirements
These requirements apply when deploying more than one server. Approval caches remain independent and in-memory on each server.

- **Master key**: LAN and VPS must use the same raw 32-byte master key file. Both servers use that key directly for AES-256-GCM and derive the same instance tag from it. A client can seal through one server and unseal through the other.
- **Device config**: LAN and VPS must use the same `devices[]` list in their server configs. Adding or disabling a device requires updating both configs and restarting both servers.
- **Telegram bot tokens**: LAN and VPS must use separate bot tokens when using long polling. Telegram `getUpdates` is a single queue per bot token; if both servers poll the same bot, one server will consume callbacks intended for the other. Both bots can send messages to the same chat/user.
- **Device authority**: `devices[]` in the server config is the sole device authority. Renaming a device preserves access to existing blobs for its UUID. Startup and Seal do not create device state files.

## Trust boundaries
Caddy separates externally accessible TLS connections from the loopback-only gRPC server. The metrics and health listener is also loopback-only and has no authentication. This layout relies on the host and reverse proxy remaining trusted.

The Telegram gate controls successful unsealing, not access to the master key itself. Possession of the master key and a sealed blob bypasses the gate. The key file therefore remains part of the trusted server state.

## Availability and failover
A second server provides another endpoint, but does not remove Telegram as an approval dependency. Each server has an independent approval cache. A cache hit can permit an unseal without contacting Telegram until the entry expires.

The Synology client changes endpoints only after an RPC error or an empty response. Denied, wrong-key, and decrypt-error responses contain random bytes, so a reachable server can prevent use of a later endpoint. This preserves the Talos KMS failure-response contract rather than exposing the reason for denial to callers.

See [Runtime behavior](../reference/runtime.md) for limits, deadlines, listeners, and key lifetime; see [Deploy LAN + VPS servers](../runbooks/deploy-lan-vps.md) for setup steps.
