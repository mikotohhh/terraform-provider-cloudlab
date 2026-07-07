#!/usr/bin/env bash
# One-shot GPU availability survey. Usage:
#   ./run.sh          # 7-day windows (default)
#   ./run.sh 24       # 24-hour windows
#
# Credentials are picked up automatically:
#   CLOUDLAB_TOKEN  from ~/Downloads/cloudlab.jwt unless already exported
#   cert passphrase from the macOS Keychain (via run_discovery.sh)
#
# The provider binary must exist at the repo root (go build once after clone).
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -x ../../terraform-provider-cloudlab ]; then
  echo "run.sh: provider binary missing; run 'go build' at the repo root first" >&2
  exit 1
fi

: "${CLOUDLAB_TOKEN:=$(cat "$HOME/Downloads/cloudlab.jwt")}"
export CLOUDLAB_TOKEN CHECKPOINT_DISABLE=1
export TF_CLI_CONFIG_FILE="$PWD/dev.tfrc"

args=()
if [ $# -ge 1 ]; then
  args+=(-var "duration_hours=$1")
fi
exec terraform apply -auto-approve "${args[@]}"
