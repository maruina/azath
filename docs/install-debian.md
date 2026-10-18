# Install azath on Debian 13
azath ships a root-run installer for Debian 13 servers on `amd64` and `arm64`. It installs the binary and a managed systemd unit, but never enables or starts the service. Start with one LAN server whose storage, networking, and DNS do not depend on shares it unlocks. Configuration, Caddy, and optional LAN+VPS redundancy are covered in [Deploy LAN + VPS](runbooks/deploy-lan-vps.md).

## Prerequisites
- Debian 13, `amd64` or `arm64`.
- Root. The installer refuses to run otherwise.
- `curl`, `tar`, `sha256sum`, `mktemp`, `awk`, `chmod`, `chown`, `cp`, `grep`, `gzip`, `mkdir`, `mv`, `rm`, `tr`, `wc`. No Python, jq, 1Password CLI, or GitHub token required.
- `id`, `uname`, `cmp`, and `systemctl`. Creating the service account also needs `getent`, `groupadd`, and `useradd`.
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

For accounts, file permissions, and service restart rules, see [Installer behavior](reference/installer.md#debian).

## Verify
```sh
azath --version
```

If a unit was installed but the service is not yet configured, that is expected — the installer does not start azath.

## Rollback
Use the latest installer with a pinned previous binary:

```sh
printf 'Previous release tag: '
read -r AZATH_VERSION
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION="$AZATH_VERSION" sh
```

If the latest installer itself is broken, pin both installer and binary to a compatible release:

```sh
printf 'Compatible release tag: '
read -r AZATH_VERSION
curl -fsSL "https://github.com/maruina/azath/releases/download/$AZATH_VERSION/install.sh" | AZATH_VERSION="$AZATH_VERSION" sh
```

Checksums detect archive corruption but share the GitHub release trust boundary with the download. No unattended updater is installed.

## Next steps
- [Deploy LAN + VPS servers](runbooks/deploy-lan-vps.md) — server config, Caddy TLS, systemd startup, failover verification.
- [Talos Linux disk encryption](runbooks/talos.md) — configuring Talos device identities and configuring the KMS key slot.
- The managed unit lives at [`deploy/systemd/azath.service`](../deploy/systemd/azath.service); the production runbook explains the `EnvironmentFile=/etc/azath/onepassword.env` requirement.