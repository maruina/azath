# azath
Homelab key management service (KMS) for [Talos Linux](https://www.talos.dev/) disk encryption and [Synology](https://www.synology.com/) encrypted shared folders.

azath seals secrets with AES-256-GCM and requires Telegram approval or an in-memory approval-cache hit for every successful unseal. Disabling a device in server config denies sealing and unsealing for that device after the config is deployed and every server is restarted.

Run one LAN server or a redundant LAN + VPS pair with the same master key and device list. Caddy terminates TLS in front of loopback-only gRPC listeners. A server's storage, networking, and DNS must not depend on a shared folder it unlocks.

## Install and deploy
The root-run installer supports Debian 13 servers and Synology DSM 7 clients on `amd64` and `arm64`.

- [Install on Debian 13](docs/install-debian.md)
- [Install on Synology DSM 7](docs/install-synology.md)
- [Deploy LAN + VPS servers](docs/runbooks/deploy-lan-vps.md)
- [Unlock Synology encrypted shares](docs/runbooks/synology.md)
- [Configure Talos disk encryption](docs/runbooks/talos.md)

## Documentation
See the [documentation index](docs/README.md) for installation, operations, development, and architecture.

- [Build and test](docs/contributing.md)
- [Publish a release](docs/releasing.md)
- [Architecture and threat model](docs/architecture/mvp.md)
- [Security policy and vulnerability reporting](docs/security.md)
- [Agent instructions](AGENTS.md)

Implementation plans are under [`plans/`](plans/). All other documentation is under [`docs/`](docs/).
