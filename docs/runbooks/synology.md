# Unlock Synology encrypted shares
Run one dedicated command for all configured shares. The command waits for Telegram approval of the original request, then mounts each share sequentially. Later shares can use the same device approval while its cache entry remains valid. `gate.telegram.approval_cache_ttl` defaults to `2m`; explicit values must be positive. The server config does not accept `0s`.

## Prepare the NAS
Install the binary with [the DSM installer](../install-synology.md). Keep the binary, config, sealed blobs, and boot helper on storage available before the encrypted shares mount. The server's storage, DNS, and network must not depend on these shares.

## Configure identity and shares
Use `/etc/azath/client-synology.yaml` for both `seal` and `synology`:

```yaml
device:
  name: synology
  uuid: 550e8400-e29b-41d4-a716-446655440000
endpoints:
  - https://azath-lan.example:443
shares:
  - name: homes
    sealed_blob: /etc/azath/secrets/homes.key.sealed
  - name: media
    sealed_blob: /etc/azath/secrets/media.key.sealed
```

Configure the same UUID as an enabled device on every server (generated via `azath config new-device --name synology --config /etc/azath/server.yaml`). Names must be unique within `shares`. Use one reliable LAN endpoint first. Additional endpoints provide transport failover, not independent retries of a failed mount. Servers have independent approval caches.

## Seal each passphrase
On a trusted machine, use the same client config. Create `/etc/azath/secrets` before writing output; `seal --out` does not create parent directories:

```sh
azath seal --config /etc/azath/client-synology.yaml --out /etc/azath/secrets/homes.key.sealed --prompt
```

Repeat for `media` with its own passphrase and output path. Transfer only the config and sealed blobs to the NAS. Keep config and blobs root-owned and restrict access; see [deployment permissions](../deployment.md#backup).

## Unlock and schedule at boot
Trust the Caddy CA in the trust store used by the azath process before connecting. The client uses system certificate verification and TLS 1.3 or later; it has no custom-CA flag. DSM trust-store integration needs validation on the target NAS.

Run as root on DSM:

```sh
azath synology --config /etc/azath/client-synology.yaml
```

Approve the waiting request in Telegram. The per-endpoint deadline is 10 minutes; `--timeout` accepts a positive duration up to 30 minutes. Each mount has a one-minute execution deadline. The command prints only share names and outcome codes, continues after a failed share, and returns nonzero if any share fails. Cancellation stops further mounts.

Install `deploy/synology/azath-unseal.sh` as `/etc/azath/azath-unseal.sh`, make it executable, and create a root Boot-up task in DSM Control Panel > Task Scheduler. The helper makes one invocation and writes safe results to `/var/log/azath-unseal.log`. It does not retry mounts. Review the results before a manual rerun because a rerun can attempt already mounted shares.

## Handle failures
If a share fails, inspect the reported outcome and correct the server, config, or blob problem before a manual rerun. A nonempty RPC response ends endpoint failover for that share, and a rerun can attempt shares that are already mounted.

For input limits, endpoint failover rules, and DSM passphrase exposure, see [Client integration](../reference/clients.md#synology).

Live DSM 7 and DS420+ behavior remains unverified. Container tests verify command wiring with a fixed fake DSM executable, not NAS compatibility.
