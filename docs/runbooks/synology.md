# Unlock Synology encrypted shares
Run one dedicated command for all configured shares. The command waits for Telegram approval of the original request, then mounts each share sequentially. A positive device approval-cache TTL lets later shares use the same approval. TTL zero requires a separate approval for each request.

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
On a trusted machine, use the same client config:

```sh
azath seal --config /etc/azath/client-synology.yaml --out /etc/azath/secrets/homes.key.sealed --prompt
```

Repeat for `media` with its own passphrase and output path. Transfer only the config and sealed blobs to the NAS. Keep config and blobs root-owned and restrict access; see [deployment permissions](../../deploy/README.md).

## Unlock and schedule at boot
Run as root on DSM:

```sh
azath synology --config /etc/azath/client-synology.yaml
```

Approve the waiting request in Telegram. The per-endpoint deadline is 10 minutes; `--timeout` accepts a positive duration up to 30 minutes. Each mount has a one-minute execution deadline. The command prints only share names and outcome codes, continues after a failed share, and returns nonzero if any share fails. Cancellation stops further mounts.

Install `deploy/synology/azath-unseal.sh` as `/etc/azath/azath-unseal.sh`, make it executable, and create a root Boot-up task in DSM Control Panel > Task Scheduler. The helper makes one invocation and writes safe results to `/var/log/azath-unseal.log`. It does not retry mounts. Review the results before a manual rerun because a rerun can attempt already mounted shares.

## Failure and security contract
The command executes only `/usr/syno/sbin/synoshare --enc_mount NAME PASSPHRASE`, directly without a shell. There is no executable override or arbitrary argument support. Subprocess stdout, stderr, and errors are not printed because DSM diagnostics can contain arguments. DSM requires a passphrase argument, which can be visible to privileged process inspection; Go cannot reliably clear the argument string.

Config input is limited to 64 KiB, 64 shares, and eight endpoints. Share names use 1–80 ASCII letters, digits, underscores, dots, spaces, or hyphens and start with a letter, digit, or underscore. Duplicate names, unknown fields, noncanonical UUIDs, invalid endpoints, and trailing YAML documents are rejected before mounts. Blob files are read with a 16 KiB limit and must decode to 32–4128 bytes. Passphrases must be at most 4096 bytes and contain no NUL.

Only an RPC error or an empty response permits endpoint failover. A nonempty response ends failover for that share, even if mounting fails. Denial, disabled devices, wrong keys, and decrypt errors produce random bytes under the KMS protocol; the client cannot identify these from the response. Correct the server/config/blob problem instead of relying on a second endpoint. Invalid local blob encoding is reported as `invalid_blob` without an RPC.

Live DSM 7 and DS420+ behavior remains unverified. Container tests verify command wiring with a fixed fake DSM executable, not NAS compatibility.
