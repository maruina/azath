#!/bin/sh
#
# azath unseal script for Synology DSM.
# Schedule via Control Panel > Task Scheduler > Boot-up.
#
# One azath client invocation per encrypted shared folder.
# Uncomment or add lines for each share.

set -eu

LOG="/var/log/azath-unseal.log"
AZATH="/usr/local/bin/azath"
CONFIG="/etc/azath/client-synology.yaml"

exec >>"$LOG" 2>&1
echo "[$(date -Iseconds)] Starting azath unseal..."

# Run each share independently so a failure (Telegram deny, azath down, wrong
# blob) does not abort the remaining shares.
unseal_share() {
  share="$1"; blob="/etc/azath/secrets/${share}.key.sealed"
  if "$AZATH" client \
      --config "$CONFIG" \
      --sealed-blob "$blob" \
      -- /usr/syno/sbin/synoshare --enc_mount "$share"; then
    echo "[$(date -Iseconds)] ${share}: unsealed."
  else
    rc=$?
    echo "[$(date -Iseconds)] ${share}: FAILED (exit ${rc})."
  fi
}

unseal_share homes
# Add more shares:
# unseal_share documents
# unseal_share photos

echo "[$(date -Iseconds)] Azath unseal complete."
