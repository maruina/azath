# Synology unseal flow

This runbook covers setting up azath to unseal encrypted Synology shared folders at boot.

## Architecture

Synology runs `azath client` once per encrypted shared folder at boot. Each invocation:

1. Reads a sealed blob (produced by `azath seal`).
2. Calls the azath KMS `Unseal` RPC through a TLS endpoint.
3. Appends the unsealed passphrase as the final argument to `synoshare --enc_mount`.

The plaintext passphrase exists only as a process argument for the duration of the `synoshare` command. It is never written to disk.

## Prerequisites

- An azath server pair (LAN + VPS) deployed and healthy.
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
  - https://azath-vps.example.com:443
  - https://azath-lan.example.internal:443
```

Order endpoints with the most reliable first. The client tries endpoints in order; if the first fails, it falls back.

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

### Security trade-off

The unsealed passphrase appears as the final argument to `synoshare` and is visible in `/proc/pid/cmdline` for the duration of the process. This is an unavoidable consequence of the `synoshare` interface: it requires the passphrase as a command argument.

Mitigations:

- The passphrase is never written to disk.
- `azath client` does not log command arguments.
- The process lifetime is short (sub-second for `synoshare`).
- DSM Task Scheduler runs as root; restrict access to the Synology shell.

## Step 6: Test

Manually test one invocation:

```bash
azath client \
  --config /etc/azath/client-synology.yaml \
  --sealed-blob /etc/azath/secrets/homes.key.sealed \
  -- /usr/syno/sbin/synoshare --enc_mount homes
```

Expected behavior:

- First run: Telegram approval required, then volume mounts.
- Subsequent runs within approval cache TTL: volume mounts without Telegram prompt.
- Stopping then re-mounting: idempotent, exit code 0.
- Wrong sealed blob or unreachable servers: exit code 1.

## Step 7: Enable boot task

In DSM Task Scheduler:

1. Create a **Triggered Task** → **Boot-up**.
2. Set **User** to `root`.
3. Set **Command** to `/usr/local/bin/azath-unseal.sh`.
4. Enable the task.
5. Reboot and verify the encrypted folders mount automatically.

## Idempotency note

`synoshare --enc_mount` is idempotent: running it on an already-mounted share returns exit code 0. Running it with the wrong passphrase returns exit code 2.
