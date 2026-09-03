# Deploy
azath releases include a root-run installer for Debian 13 servers and Synology DSM 7 clients on `amd64` and `arm64`.

## Install or upgrade
The repository and release assets must be public. Run the release-distributed installer as root; it does not leave a local installer behind:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | sh
```

To install a specific binary release, apply `AZATH_VERSION` to `sh`, not `curl`:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v0.2.0 sh
```

The installer needs `curl`, `tar`, `sha256sum`, `mktemp`, and standard POSIX file utilities. It does not require Python, jq, 1Password CLI, GitHub tokens, or a configurable install directory. It installs `/usr/local/bin/azath`.

On Debian, a fresh install creates the `azath` service identity, missing `/etc/azath` directory, and managed unit in `/usr/local/lib/systemd/system`. It never enables or starts the service. A changed binary restarts only a service that was active before the replacement. On DSM, the installer creates only missing root-only azath directories and the binary; client configuration, sealed blobs, the helper, and Task Scheduler remain operator-owned.

Checksums detect corruption but share the GitHub release trust boundary with the downloaded archive. No unattended updater is installed.

## Rollback
Use the latest compatible installer with a pinned binary:

```sh
curl -fsSL https://github.com/maruina/azath/releases/latest/download/install.sh | AZATH_VERSION=v<previous> sh
```

If the latest installer is broken, pin both installer and binary to a compatible release:

```sh
curl -fsSL https://github.com/maruina/azath/releases/download/v<previous>/install.sh | AZATH_VERSION=v<previous> sh
```

A release predating the compatible installer, such as `v0.1.0`, cannot provide the second form. The installer supports its legacy binary archive name for rollback.

## Publication and rollout gates
Before making the repository public, run the history audit from a full clone:

```sh
docker run --rm -v "$PWD:/repo" -w /repo zricethezav/gitleaks:v8.30.1 git --redact --no-banner --exit-code 1 .
```

Require `gh repo view maruina/azath --json visibility --jq .visibility` to print `PUBLIC` and `gh api repos/maruina/azath --jq '.security_and_analysis.secret_scanning.status'` to print `enabled`. After the first compatible tag, verify authenticated release metadata with `gh release view v<version> --json assets` and download `install.sh`, `checksums.txt`, and both archives through unauthenticated latest and tag-pinned URLs before deleting the temporary smoke-test directory.

Smoke-test a fresh Debian 13 host, confirm the service remains inactive, then rerun the installer to confirm the binary is unchanged. Upgrade one active redundant server, verify its new identity and `curl http://127.0.0.1:9090/readyz`, and only then upgrade the second server. On DSM, record hashes and metadata for config, blobs, helper, and Task Scheduler ownership before install and rerun; all must remain unchanged.

## Runbooks
- [LAN and VPS server deployment](../docs/runbooks/deploy-lan-vps.md)
- [Synology client deployment](../docs/runbooks/synology.md)

## Backup
Back up these files regularly and transfer them securely:

| File | Permission | Notes |
|---|---|---|
| Master key | `0600` | `internal keymanager` source file |
| Server config | `0640` | `/etc/azath/server.yaml` |
| Client config | `0640` | `/etc/azath/client-synology.yaml` |
| Sealed blobs | `0600` | `/etc/azath/secrets/*.sealed` |
| Seal bearer token | `0600` | Re-issue via 1Password; re-seal affected blobs after rotation. |
| Telegram bot token | `0600` | Re-issuable via @BotFather; use separate LAN and VPS bots. |
| Caddy config | `0640` | `/etc/caddy/Caddyfile` |
| 1Password service-account token | `0600` | `/etc/azath/onepassword.env` |
