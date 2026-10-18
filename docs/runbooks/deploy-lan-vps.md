# Deploy LAN + VPS
This runbook walks through deploying two azath servers — one on your local LAN (e.g. an LXC container) and one on a VPS (e.g. a cloud VM) — plus configuring Caddy for TLS termination.

## Single LAN server
For an initial deployment, use one LAN host and one Telegram bot. Follow Step 1, the LAN configuration in Step 2, the LAN Caddy setup in Step 3, and Steps 4–6. Skip the VPS configuration, cross-server comparisons, key copying, and Step 7 until adding a second server. Keep only the LAN endpoint in the client config.

The LAN host's storage, networking, and DNS must not depend on an encrypted NAS shared folder. Keep a manual NAS unlock method available if this server or Telegram is unavailable.

The prerequisites below describe the redundant two-server deployment.

## Prerequisites
- Two Linux machines with `systemd`, one on LAN and one on VPS.
- A domain or subdomain pointing to your VPS public IP.
- Two Telegram bot tokens (one for LAN, one for VPS) created via [@BotFather](https://t.me/botfather).
- Your Telegram user ID and chat ID for approval messages.
- A raw 32-byte master key file.
- The 1Password CLI (`op`) and a service account token.

Install azath on both servers with Step 1 before creating files under `/etc/azath`. Run the setup commands as root.

## Step 1: Install azath
Install on each Debian 13 server. See [install-debian.md](../install-debian.md) for prerequisites, what the installer creates, and rollback. Run the public release installer as root:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | sh
```

Pin a binary release by applying `AZATH_VERSION` to `sh`:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v0.2.0 sh
```

A fresh install creates the account, missing `/etc/azath` directory, and vendor unit, but never enables or starts the service. A same-identity rerun does not restart it. A different `--version` identity restarts only a service that was active before replacement. If the installer cannot determine service state, it stops before replacing the binary. If a restart fails, inspect `systemctl status azath.service --no-pager` and roll back:

```sh
printf 'Previous release tag: '
read -r AZATH_VERSION
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION="$AZATH_VERSION" sh
```

Verify the binary with `azath --version`.

## Step 2: Configure servers
### 1Password service account
azath resolves secrets from 1Password via `op://` references. The systemd unit loads the token from `EnvironmentFile=/etc/azath/onepassword.env` (see `deploy/systemd/azath.service`).

1. Install the `op` CLI on both servers:
   ```bash
   # Follow the official guide for your distro:
   # https://developer.1password.com/docs/cli
   op --version
   ```
2. In 1Password, create a service account scoped to the vault holding your azath items. Grant it read access to every item referenced by a `*_ref` field, for example:
   - `op://<vault>/azath-lan-bot/token`
   - `op://<vault>/azath-vps-bot/token`
3. On each server, write the service account token to `/etc/azath/onepassword.env`:
   ```bash
   install -m 0600 -o root -g root /dev/null /etc/azath/onepassword.env
   printf 'OP_SERVICE_ACCOUNT_TOKEN=%s\n' "ops_..." > /etc/azath/onepassword.env
   chmod 0600 /etc/azath/onepassword.env
   ```
   The unit refuses to start if this file is missing.

### Generate master key
```bash
umask 077
dd if=/dev/urandom of=/etc/azath/master.key bs=32 count=1
chown azath:azath /etc/azath/master.key
chmod 0600 /etc/azath/master.key
```

Generate the key once and copy it to the other server. It must be identical on LAN and VPS. Set ownership to `azath:azath` and mode `0600` on both copies. Do not regenerate a key after creating sealed blobs; keep an offline recovery copy.

Start with an empty `devices: []` placeholder on the LAN server, then bootstrap the first device with `azath config new-device`. Reuse the generated UUID verbatim on the VPS so the device lists match.

### LAN server config
File: `/etc/azath/server.yaml`

```yaml
server:
  name: lan
  listen: "127.0.0.1:7800"
  metrics_listen: "127.0.0.1:9090"
  log_level: info
  log_format: json

master_key:
  source: file
  path: /etc/azath/master.key

# Bootstrap: start empty, then add the first device below.
devices: []

gate:
  type: telegram
  telegram:
    bot_token_ref: "op://<vault>/azath-lan-bot/token"
    chat_id: "123456789"
    authorized_user_id: "987654321"
    rate_limit: 5m
    approval_ttl: 10m
    approval_cache_ttl: 2m
```

Add the first device and record the printed UUID:

```bash
azath config new-device --name synology --config /etc/azath/server.yaml
```

Validate:

```bash
azath config validate /etc/azath/server.yaml
```

### VPS server config
File: `/etc/azath/server.yaml`

```yaml
server:
  name: vps
  listen: "127.0.0.1:7800"
  metrics_listen: "127.0.0.1:9090"
  log_level: info
  log_format: json

master_key:
  source: file
  path: /etc/azath/master.key

# Reuse the LAN-generated UUID verbatim.
devices:
  - name: synology
    uuid: <lan-generated-uuid>
    disabled: false

gate:
  type: telegram
  telegram:
    bot_token_ref: "op://<vault>/azath-vps-bot/token"  # different bot token
    chat_id: "123456789"
    authorized_user_id: "987654321"
    rate_limit: 5m
    approval_ttl: 10m
    approval_cache_ttl: 2m
```

Validate:

```bash
azath config validate /etc/azath/server.yaml
```

### Compare the device lists
Compare the `devices` entries in the two server configs. Names, UUIDs, and disabled states must match. The server names and Telegram bot token references differ between servers.

## Step 3: Configure Caddy
### LAN Caddyfile
File: `/etc/caddy/Caddyfile`

```
azath-lan.example.internal {
    tls internal
    reverse_proxy h2c://127.0.0.1:7800
}
```

This matches the internal-CA setup in `deploy/caddy/Caddyfile.lan.example`. Install and configure Caddy separately; the azath installer does not install it. Clients must trust its root CA. The azath CLI uses system certificate verification; importing a certificate in DSM's UI has not been verified to update the trust store used by the Go client.

### VPS Caddyfile
File: `/etc/caddy/Caddyfile`

```
azath-vps.example.com {
    reverse_proxy h2c://127.0.0.1:7800
}
```

Caddy automatically provisions a Let's Encrypt certificate for the public domain.

## Step 4: Configure systemd
See `deploy/systemd/azath.service` for a production systemd unit. The service must:

- Run as a non-root user (e.g. `azath`).
- Bind only to loopback addresses.
- Use `Restart=on-failure` so transient outages retry.
- Set `LimitMEMLOCK=infinity` for `mlockall`.
- Load `EnvironmentFile=/etc/azath/onepassword.env` so 1Password resolves `*_ref` fields.

The unit fails to start if `/etc/azath/onepassword.env` is missing.

## Step 5: Start services and verify `op` auth
Before starting the service, set server config ownership and check access on each host:

```sh
chown azath:azath /etc/azath/server.yaml
chmod 0600 /etc/azath/server.yaml
runuser -u azath -- test -r /etc/azath/server.yaml
runuser -u azath -- test -r /etc/azath/master.key
runuser -u azath -- test -w /etc/azath
```

Keep `/etc/azath/onepassword.env` root-owned with mode `0600`; systemd reads it before starting the service.

On both servers:

```bash
systemctl daemon-reload
systemctl enable --now azath
systemctl enable --now caddy
```

Verify the health endpoint returns ready before upgrading the redundant server:

```bash
curl http://127.0.0.1:9090/readyz
```

If `/readyz` is not ready or `op read` resolution fails at startup, inspect the logs:

```bash
journalctl -u azath
```

Do not test `op read` with `sudo -u env op read ...`; the unit's `ProtectHome=yes` makes that form fragile.

## Step 6: Create sealed blobs
Use `azath seal` from a trusted provisioning machine (not the Synology or Talos host). Use the shared `device`, `endpoints`, and `shares` schema from [the Synology runbook](synology.md). The Seal RPC has a 30-second deadline:

```bash
azath seal \
  --config /etc/azath/client.yaml \
  --endpoint https://azath-lan.example.internal:443 \
  --out /etc/azath/secrets/homes.key.sealed \
  --prompt
```

## Step 7: Verify failover
On a disposable test share, stop the LAN server and confirm the client successfully unseals through the VPS. Configure endpoints in LAN-first order. Each endpoint has a 10-minute approval deadline by default; `--timeout` accepts a positive duration up to 30 minutes. This command mounts every configured share, so do not run it against mounted production shares:

```bash
azath synology --config /etc/azath/client.yaml
```

## Apply device changes
The server reads devices only at startup. After adding, enabling, or disabling a device, deploy matching device lists to every server, restore config ownership to `azath:azath` with mode `0600`, and restart each azath process. Validate the config before restarting. For a stolen device, apply the disablement to every server; a server with the old config can still approve it.

## Rotate the 1Password service-account token
When the service account token is rotated:

1. Update `/etc/azath/onepassword.env` on both servers with the new token.
2. Restart one server at a time and verify its `/readyz` response before restarting the other. `/readyz` checks the loaded key, not live Telegram connectivity:
   ```bash
   sudo systemctl restart azath
   ```
3. Verify `/readyz` returns ready on both servers.

## Failure modes
### LAN down, VPS works
Client falls back to the VPS endpoint. Telegram approval still required.

### VPS down, LAN works
With LAN-first endpoint order, the client uses LAN without contacting the VPS.

### Telegram unavailable
Without a valid approval-cache entry, both LAN and VPS servers fail closed: Unseal requests return `codes.OK` with random bytes. An unexpired cache entry can still permit unsealing for its device. Restore Telegram access before requests need fresh approval.

### Wrong master key on one server
Unseal returns random bytes. A nonempty response ends failover for that share. The client rejects invalid passphrases or reports mount failure, then continues to later shares and returns nonzero. A manual rerun starts at the first endpoint and can attempt already mounted shares again. Correct the key mismatch rather than relying on failover.

### Disabled device
Both servers reject Seal with `codes.PermissionDenied` and Unseal with random bytes, without sending Telegram prompts.

### 1Password service-account token expired or missing
The unit fails to start if `/etc/azath/onepassword.env` is missing. If the token is invalid or the service account lacks vault access, azath exits during secret resolution before opening its health listener; systemd retries it. Inspect the resolution error in `journalctl -u azath`. Fix the token or permissions, then restart the service.
