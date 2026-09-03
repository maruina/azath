# Install azath on Debian 13

azath ships a root-run installer for Debian 13 servers on `amd64` and `arm64`. It installs the binary and a managed systemd unit, but never enables or starts the service. Configuration, Caddy, and the LAN+VPS topology are covered in [Deploy LAN + VPS](runbooks/deploy-lan-vps.md).

## Prerequisites

- Debian 13, `amd64` or `arm64`.
- Root. The installer refuses to run otherwise.
- `curl`, `tar`, `sha256sum`, `mktemp`, `awk`, `chown`, `cp`, `grep`, `gzip`, `mkdir`, `mv`, `rm`, `tr`, `wc`. No Python, jq, 1Password CLI, or GitHub token required.
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

## What the installer creates on Debian

- The `azath` system group and `azath` system user (`nologin` shell, no home) if missing.
- `/etc/azath` (mode `0700`, owned `azath:azath`) if missing.
- The systemd unit at `/usr/local/lib/systemd/system/azath.service` (mode `0644`, owned `root:root`), followed by `systemctl daemon-reload`.
- The binary at `/usr/local/bin/azath`.

The installer never enables or starts the service. If the binary changes and the service was active before the replacement, it restarts it; if it was inactive, it leaves it inactive. If the installer cannot determine the service state, it stops before replacing the binary. If a restart fails, it prints the previous identity and a rollback command.

## Verify

```sh
azath --version
```

If a unit was installed but the service is not yet configured, that is expected — the installer does not start azath.

## Rollback

Use the latest installer with a pinned previous binary:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v<previous> sh
```

If the latest installer itself is broken, pin both installer and binary to a compatible release:

```sh
curl -fsSL https://github.com/maruina/azath/releases/download/v<previous>/install.sh | AZATH_VERSION=v<previous> sh
```

Checksums detect archive corruption but share the GitHub release trust boundary with the download. No unattended updater is installed.

## Next steps

- [Deploy LAN + VPS servers](runbooks/deploy-lan-vps.md) — server config, Caddy TLS, systemd startup, failover verification.
- [Talos Linux disk encryption](runbooks/talos.md) — registering Talos nodes and configuring the KMS key slot.
- The managed unit lives at [`deploy/systemd/azath.service`](../deploy/systemd/azath.service); the production runbook explains the `EnvironmentFile=/etc/azath/onepassword.env` requirement.