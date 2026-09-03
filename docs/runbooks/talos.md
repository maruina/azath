# Talos Linux disk encryption with azath

This runbook covers configuring Talos Linux nodes to use azath as a KMS for disk encryption key sealing and unsealing.

## Architecture

Talos uses a [KMS key slot](https://www.talos.dev/v1.10/talos-guides/configuration/disk-encryption/#kms-key-slot) in the disk encryption configuration. At boot, Talos calls the azath `Seal` and `Unseal` gRPC methods through the configured endpoint.

## Prerequisites

- An azath server pair (LAN + VPS) deployed and healthy.
- A device (node) entry for each Talos node in the azath server config.
- Talos v1.7 or later with native KMS support.

## Step 1: Register Talos nodes

Add a device entry for each Talos node in both LAN and VPS server configs:

```yaml
devices:
  - name: talos-worker-01
    uuid: 660e8400-e29b-41d4-a716-446655440001
    disabled: false
  - name: talos-control-01
    uuid: 770e8400-e29b-41d4-a716-446655440002
    disabled: false
```

Use `azath config new-device` to generate UUIDs:

```bash
azath config new-device --name talos-worker-01 --config /etc/azath/server.yaml
```

Deploy the updated config to both servers and restart azath.

## Step 2: Configure Talos KMS

Edit the Talos machine config to use the azath KMS endpoint. Example for `talosctl edit mc`:

```yaml
machine:
  encryption:
    - keyProvider:
        - kms:
            endpoint: https://azath-vps.example.com:443
            node_uuid: "660e8400-e29b-41d4-a716-446655440001"
```

### Failover

**Unverified**: native Talos KMS failover between multiple endpoints is not tested in the current MVP. Talos v1.7+ supports a single KMS endpoint per key slot. If that endpoint is unavailable at boot, the node will not unseal its disk.

Options for failover:

1. **Single DNS name with multiple A records**: configure a DNS name that resolves to both LAN and VPS IPs. Talos tries addresses in order until one succeeds.
2. **Single VPS endpoint**: rely on the VPS being more reliable than the LAN. Accept that LAN-side sealing/unsealing always traverses the internet.
3. **Separate config per location**: use different machine configs for nodes behind the LAN and nodes behind the VPS.

The MVP explicitly does not claim native failover. If you test and validate a specific failover strategy, update this document.

## Step 3: Apply the config

```bash
talosctl apply-config -f patched-config.yaml
talosctl reboot
```

At boot, Talos calls azath `Unseal` to get the disk encryption key. The first unseal requires Telegram approval; subsequent unseals within the approval cache TTL are approved automatically.

## Talos-specific considerations

### Mutual TLS

azath authenticates Talos using a bearer token (the `seal_token_ref` in server config). Talos does not support custom bearer tokens for KMS. If Talos does not send an `authorization` metadata header with the KMS request, the `Seal` RPC will fail with `unauthenticated`.

**Verified**: this applies to the `Seal` path only. `Unseal` does not require a bearer token and succeeds without one.

### Boot timing

If Telegram is unavailable at boot:

- `Unseal` returns `codes.OK` with random bytes.
- Talos gets a wrong encryption key and fails to unlock the disk.
- The node will not boot until the key is re-provisioned or Telegram becomes available and the admin approves.

To recover: ensure Telegram is reachable before booting Talos nodes.

### Sealed blobs

Unlike the Synology flow, Talos does not use `azath seal` or `azath client`. Talos calls `Seal` internally when provisioning the encryption key with `kms` key slot. The sealed blob is stored in the Talos metadata partition, not in a file.

## Re-provisioning after master key rotation

If the azath master key changes, Talos nodes cannot unseal their disks because the old sealed blobs are tied to the old key. Steps to recover:

1. Deploy the new master key to both LAN and VPS servers.
2. Re-provision each Talos node's disk encryption (back up data first).
3. Approve the `Seal` calls in Telegram as Talos re-encrypts.
