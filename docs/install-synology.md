# Install azath on Synology DSM 7

azath ships a root-run installer for Synology DSM 7 clients on `amd64` and `arm64`. On DSM the installer installs only the client binary and root-only config directories — no service account, no systemd integration. The full unseal flow (sealed blobs, client config, boot task) is covered in [Synology unseal flow](runbooks/synology.md).

## Prerequisites

- DSM 7, `amd64` or `arm64`.
- Root. The installer refuses to run otherwise.
- `curl`, `tar`, `sha256sum`, `mktemp`, `awk`, `chown`, `cp`, `grep`, `gzip`, `mkdir`, `mv`, `rm`, `tr`, `wc`.
- The repository and release assets must be public.

## Install

Run the release-distributed installer as root:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | sh
```

To pin a binary release, apply `AZATH_VERSION` to `sh`, not `curl`:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v0.2.0 sh
```

## What the installer creates on DSM

- `/etc/azath` and `/etc/azath/secrets` (mode `0700`, owned `root:root`) if missing.
- The binary at `/usr/local/bin/azath`.

The installer does not create a service account or any systemd or Task Scheduler integration. Reruns preserve existing config, sealed blobs, the unseal helper, and Task Scheduler entries.

## Verify

```sh
azath --version
```

## Rollback

Reinstall a pinned previous binary with the same installer:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v<previous> sh
```

If the latest installer itself is broken, pin both installer and binary to a compatible release:

```sh
curl -fsSL https://github.com/maruina/azath/releases/download/v<previous>/install.sh | AZATH_VERSION=v<previous> sh
```

Checksums detect archive corruption but share the GitHub release trust boundary with the download. No unattended updater is installed.

## Next steps

- [Synology unseal flow](runbooks/synology.md) — creating sealed blobs, client config, the boot task, and the `synoshare --enc_mount` security trade-off.