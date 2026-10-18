# Client integration
## Synology
The command executes only `/usr/syno/sbin/synoshare --enc_mount NAME PASSPHRASE`, directly without a shell. There is no executable override or arbitrary argument support. Subprocess stdout, stderr, and errors are not printed because DSM diagnostics can contain arguments. DSM requires a passphrase argument, which can be visible to privileged process inspection; Go cannot reliably clear the argument string.

Config input is limited to 64 KiB, 64 shares, and eight endpoints. Share names use 1–80 ASCII letters, digits, underscores, dots, spaces, or hyphens and start with a letter, digit, or underscore. Duplicate names, unknown fields, noncanonical UUIDs, invalid endpoints, and trailing YAML documents are rejected before mounts. Blob files are read with a 16 KiB limit and must decode to 32–4128 bytes. Passphrases must be at most 4096 bytes and contain no NUL.

Only an RPC error or an empty response permits endpoint failover. A nonempty response ends failover for that share, even if mounting fails. Denial, disabled devices, wrong keys, and decrypt errors produce random bytes under the KMS protocol; the client cannot identify these from the response. Invalid local blob encoding is reported as `invalid_blob` without an RPC.

## Talos
### Authorization
azath authorizes Talos requests via the server's configured `devices[]` list. When Talos calls `Seal` during initial partition encryption or `Unseal` during boot, it transmits its node UUID. If the node UUID is configured and enabled in `server.yaml`, azath serves the request. `Unseal` additionally requires Telegram approval or a valid device-scoped approval-cache entry.

### Boot timing
If Telegram is unavailable at boot and there is no valid approval-cache entry:

- `Unseal` returns `codes.OK` with random bytes.
- Talos gets a wrong encryption key and fails to unlock the disk.
- The node will not boot until the key is re-provisioned or Telegram becomes available and the admin approves.

For recovery steps, see [Configure Talos disk encryption](../runbooks/talos.md).

### Sealed blobs
Unlike the Synology flow, Talos does not use `azath seal` or `azath synology`. Talos calls `Seal` internally when provisioning the encryption key with `kms` key slot. Talos manages the sealed key's on-disk storage; azath does not write a client blob file for it. The exact storage layout is outside this repository and depends on the Talos version.

See [Runtime behavior](runtime.md) for RPC deadlines and approval-cache behavior. See [Unlock Synology encrypted shares](../runbooks/synology.md) or [Configure Talos disk encryption](../runbooks/talos.md) for setup steps.
