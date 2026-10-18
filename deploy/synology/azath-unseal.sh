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

# The server prompts Telegram on the first unseal attempt and returns an
# unusable placeholder until the approval is granted (or the approval cache
# answers). Retry so a fresh boot succeeds after approval without help.
ATTEMPTS=5
SLEEP_SECONDS=15

exec >>"$LOG" 2>&1
echo "[$(date -Iseconds)] Starting azath unseal..."

# Run each share independently so a failure (Telegram deny, azath down, wrong
# blob) does not abort the remaining shares.
unseal_share() {
  share="$1"; blob="/etc/azath/secrets/${share}.key.sealed"
  attempt=1
  while [ "$attempt" -le "$ATTEMPTS" ]; do
    if "$AZATH" client \
        --config "$CONFIG" \
        --sealed-blob "$blob" \
        -- /usr/syno/sbin/synoshare --enc_mount "$share"; then
      echo "[$(date -Iseconds)] ${share}: unsealed."
      return 0
    fi
    rc=$?
    echo "[$(date -Iseconds)] ${share}: attempt ${attempt}/${ATTEMPTS} failed (exit ${rc})."
    if [ "$attempt" -lt "$ATTEMPTS" ]; then
      sleep "$SLEEP_SECONDS"
    fi
    attempt=$((attempt + 1))
  done
  echo "[$(date -Iseconds)] ${share}: FAILED after ${ATTEMPTS} attempts."
  return 1
}

unseal_share homes
# Add more shares:
# unseal_share documents
# unseal_share photos

echo "[$(date -Iseconds)] Azath unseal complete."
