# Synology unseal flow

This runbook covers setting up azath to unseal encrypted Synology shared folders at boot.

## Architecture

Synology runs `azath client` once per encrypted shared folder at boot. Each invocation:

1. Reads a sealed blob (produced by `azath seal`).
2. Calls the azath KMS `Unseal` RPC through a TLS endpoint.
3. Appends the unsealed passphrase as the final argument to `synoshare --enc_mount`.

The plaintext passphrase exists only as a process argument for the duration of the `synoshare` command. It is never written to disk.

## Prerequisites

- At least one healthy azath server. One LAN server is sufficient; a VPS is optional redundancy. For the LAN-only path, follow the LAN sections of [Deploy LAN + VPS](deploy-lan-vps.md).
- The server's storage, networking, and DNS must be available without unlocking the NAS shared folders.
- A working manual unlock method and recovery passphrase or key stored independently of azath.
- One sealed blob per encrypted shared folder.
- The `azath` binary and client config on the Synology.

## Step 1: Install azath

Install on DSM 7. See [install-synology.md](../install-synology.md) for prerequisites, what the installer creates, and rollback. Run the public release installer as root:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | sh
```

To pin a binary release, apply `AZATH_VERSION` to `sh`:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v0.2.0 sh
```

The installer creates only missing `/etc/azath` and `/etc/azath/secrets` directories and installs `/usr/local/bin/azath`. Reruns preserve existing config, sealed blobs, helper, and Task Scheduler entries. It does not create accounts or systemd integration.

## Step 2: Create sealed blobs

Run `azath seal` from a trusted machine — never on the Synology itself:

```bash
azath seal \
  --config /etc/azath/client-synology.yaml \
  --endpoint https://azath-lan.example.internal:443 \
  --seal-token-file /etc/azath/seal-token \
  --out /etc/azath/secrets/homes.key.sealed \
  --prompt
```

Repeat for each shared folder (`homes`, `documents`, etc.).

## Step 3: Copy sealed blobs to Synology

Copy the sealed blobs to the Synology. Store them with mode `0600`:

```bash
scp homes.key.sealed root@synology:/etc/azath/secrets/
scp documents.key.sealed root@synology:/etc/azath/secrets/
```

## Step 4: Configure the client

File: `/etc/azath/client-synology.yaml`

```yaml
device:
  name: synology
  uuid: <synology-uuid>  # must match the UUID generated for this device in the server config

endpoints:
  - https://azath-lan.example.internal:443
```

For redundancy, add a VPS endpoint after the LAN endpoint. Order endpoints with the most reliable first. The client tries the next endpoint after an RPC failure or empty response. If a reachable server returns a nonempty placeholder and the mount command fails, the invocation stops without trying another endpoint. Retrying the helper starts with the first endpoint again. Approval caches are independent on each server.

## Step 5: Create Synology boot scripts

Synology DSM does not use systemd. Create a task in **Control Panel > Task Scheduler** that runs at boot.

Create a shell script `/usr/local/bin/azath-unseal.sh`:

```bash
#!/bin/sh

# Unseal and mount each encrypted shared folder.
# Each invocation is one sealed blob, one synoshare call.

azath client \
  --config /etc/azath/client-synology.yaml \
  --sealed-blob /etc/azath/secrets/homes.key.sealed \
  -- /usr/syno/sbin/synoshare --enc_mount homes

azath client \
  --config /etc/azath/client-synology.yaml \
  --sealed-blob /etc/azath/secrets/documents.key.sealed \
  -- /usr/syno/sbin/synoshare --enc_mount documents
```

Make it executable:

```bash
chmod 700 /usr/local/bin/azath-unseal.sh
```

`deploy/synology/azath-unseal.sh` in the repository is a fuller version of this script: it logs to `/var/log/azath-unseal.log`, retries each share, continues after a share exhausts its attempts, and exits nonzero if any share failed. Approve within its retry window; after attempts are exhausted, rerun the helper.

### Security trade-off

The unsealed passphrase appears as the final argument to `synoshare` and is visible in `/proc/pid/cmdline` for the duration of the process. This is an unavoidable consequence of the `synoshare` interface: it requires the passphrase as a command argument.

Mitigations:

- The passphrase is never written to disk.
- `azath client` does not log command arguments.
- The process lifetime is short (sub-second for `synoshare`).
- DSM Task Scheduler runs as root; restrict access to the Synology shell.

## Step 6: Test
First run the [Docker suite](../../tests/docker/README.md) to check the assembled software without using your NAS. It simulates Telegram and mounting; it does not validate DSM behavior.

On the NAS, start with one disposable encrypted shared folder. Confirm that you can still unlock it manually with azath unavailable before enabling boot automation for existing folders. The DS420+ is the initial hardware validation target, not a verified device. Record the DSM version, mount exit codes, and boot behavior during the live test.

Manually test one invocation:

```bash
azath client \
  --config /etc/azath/client-synology.yaml \
  --sealed-blob /etc/azath/secrets/homes.key.sealed \
  -- /usr/syno/sbin/synoshare --enc_mount homes
```

Expected behavior:

- First run: the server sends a Telegram prompt and returns an unusable placeholder response, so the shared folder does not mount and the command exits nonzero. This is deliberate: pending and denied responses are indistinguishable from successful ones on the wire.
- After approving in Telegram, run the same command again: the approval cache answers and the shared folder mounts.
- The shipped `azath-unseal.sh` retries each share (`ATTEMPTS` tries, `SLEEP_SECONDS` apart). The mount can succeed after approval if an attempt remains before the approval cache expires.
- Subsequent runs within approval cache TTL: the shared folder mounts without a Telegram prompt.
- Unreachable servers, an invalid blob, or a rejected passphrase: the command exits nonzero. A mount command's nonzero exit maps to client exit code 2; setup, RPC, and execution errors map to exit code 1.
- Confirm the already-mounted behavior and exact `synoshare` exit codes on your DSM version; these are not established by the Docker suite.

## Step 7: Enable boot task

In DSM Task Scheduler:

1. Create a **Triggered Task** → **Boot-up**.
2. Set **User** to `root`.
3. Set **Command** to `/usr/local/bin/azath-unseal.sh`.
4. Enable the task.
5. Reboot and verify the encrypted folders mount automatically.

## Idempotency note

Do not assume that repeating `synoshare --enc_mount` is harmless until you have checked its already-mounted behavior on the target DSM version. The Docker mount stub verifies passphrase delivery, not real Synology command semantics.
