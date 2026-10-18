#!/bin/sh
# Schedule once at boot as root in DSM Task Scheduler.
set -eu
LOG="/var/log/azath-unseal.log"
AZATH="/usr/local/bin/azath"
CONFIG="/etc/azath/client-synology.yaml"
exec >>"$LOG" 2>&1
exec "$AZATH" synology --config "$CONFIG"
