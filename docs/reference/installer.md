# Installer behavior
The release-distributed installer supports Debian 13 servers and Synology DSM 7 clients on `amd64` and `arm64`. It installs `/usr/local/bin/azath` and requires root.

## Debian
- The `azath` system group and `azath` system user (`nologin` shell, no home) if missing.
- `/etc/azath` (mode `0700`, owned by `azath` and its primary group) if missing. Existing accounts and directory permissions are preserved.
- The systemd unit at `/usr/local/lib/systemd/system/azath.service` (mode `0644`, owned `root:root`), with `systemctl daemon-reload` when the managed unit changes. A legacy binary archive does not install a unit.
- The binary at `/usr/local/bin/azath`.

The installer never enables or starts the service. If the candidate's `--version` output differs from the installed binary's output and the service was active before replacement, it restarts it; if it was inactive, it leaves it inactive. If the installer cannot determine the service state, it stops before replacing the binary. If a restart fails, it prints the previous identity and service status. It prints a rollback command when the previous identity starts with `v`. It does not automatically restore the previous binary.

## Synology DSM
- `/etc/azath` and `/etc/azath/secrets` (mode `0700`, owned `root:root`) if missing.
- The binary at `/usr/local/bin/azath`.

`azath seal` and `azath synology` share one client config with `device`, `endpoints`, and `shares` fields. The boot helper calls `azath synology` once. It unlocks shares sequentially, continues after a share failure, and returns nonzero for partial failure. One device approval can cover all shares while the approval cache window remains valid. Server config validation does not accept a zero cache TTL.

The installer does not create a service account or any systemd or Task Scheduler integration. Reruns preserve existing config, sealed blobs, the unseal helper, and Task Scheduler entries.

## Download trust
Checksums detect archive corruption but share the GitHub release trust boundary with the download. No unattended updater is installed.

For installation and rollback steps, see [Install on Debian 13](../install-debian.md) or [Install on Synology DSM 7](../install-synology.md).
