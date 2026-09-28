#!/usr/bin/env bash
# Discovery wrapper for the cloudlab_all_availability data source.
#
# Runs cloudlab_nodetypes.py. The default central Portal inventory needs no
# secret. If auto mode has to fall back to GENI, the cert-key passphrase is
# pulled from the macOS Keychain so it never lives in the repo, shell history,
# or Terraform configuration.
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
#   CLOUDLAB_DISCOVERY_TZ      timezone used by geni-lib (default: UTC)
set -euo pipefail

: "${CLOUDLAB_CERT:=$HOME/Downloads/cloudlab (2).pem}"
account="${CLOUDLAB_KEYCHAIN_ACCOUNT:-lzhou247}"
service="${CLOUDLAB_KEYCHAIN_SERVICE:-cloudlab-pass}"

if [ -z "${CLOUDLAB_PASS:-}" ]; then
  if ! CLOUDLAB_PASS="$(security find-generic-password -a "$account" -s "$service" -w 2>/dev/null)"; then
    # The default Portal inventory does not need an Emulab certificate. Keep
    # running; cloudlab_nodetypes.py will only need this secret if auto mode
    # has to fall back to strict GENI discovery.
    CLOUDLAB_PASS=""
  fi
fi

# geni-lib compares UTC credential expiry timestamps with a timezone-naive
# datetime.now(). Force UTC so users east of Greenwich are not told that a
# still-valid credential has expired early.
: "${CLOUDLAB_DISCOVERY_TZ:=UTC}"

export CLOUDLAB_PASS CLOUDLAB_CERT TZ="$CLOUDLAB_DISCOVERY_TZ"
exec python3 "$(cd "$(dirname "$0")" && pwd)/cloudlab_nodetypes.py" "$@"
