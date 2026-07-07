#!/usr/bin/env bash
# Discovery wrapper for the cloudlab_all_availability data source.
#
# Runs cloudlab_nodetypes.py with the cert-key passphrase pulled from the
# macOS Keychain, so no secret ever lives in the repo, the shell history,
# or the Terraform configuration.
#
# One-time setup (type this yourself; the passphrase is your Emulab login
# password, i.e. the one protecting your geni certificate's private key):
#
#   security add-generic-password -a <emulab-user> -s cloudlab-pass -w '<passphrase>'
#
# Overridable via environment:
#   CLOUDLAB_PASS              use this passphrase directly, skip the Keychain
#   CLOUDLAB_CERT              path to the geni certificate (cert + encrypted key)
#   CLOUDLAB_KEYCHAIN_ACCOUNT  Keychain account name   (default: lzhou247)
#   CLOUDLAB_KEYCHAIN_SERVICE  Keychain service name   (default: cloudlab-pass)
set -euo pipefail

: "${CLOUDLAB_CERT:=/Users/mikoto/Downloads/cloudlab (2).pem}"
account="${CLOUDLAB_KEYCHAIN_ACCOUNT:-lzhou247}"
service="${CLOUDLAB_KEYCHAIN_SERVICE:-cloudlab-pass}"

if [ -z "${CLOUDLAB_PASS:-}" ]; then
  if ! CLOUDLAB_PASS="$(security find-generic-password -a "$account" -s "$service" -w 2>/dev/null)"; then
    echo "run_discovery.sh: no CLOUDLAB_PASS set and no Keychain entry found." >&2
    echo "One-time setup:" >&2
    echo "  security add-generic-password -a $account -s $service -w '<your emulab passphrase>'" >&2
    exit 1
  fi
fi

export CLOUDLAB_PASS CLOUDLAB_CERT
exec python3 "$(cd "$(dirname "$0")" && pwd)/cloudlab_nodetypes.py"
