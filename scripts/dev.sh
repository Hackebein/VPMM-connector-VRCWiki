#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends curl ca-certificates
update-ca-certificates

export PATH="/usr/local/go/bin:${PATH}"

if ! command -v reflex >/dev/null 2>&1; then
  go install github.com/cespare/reflex@latest
fi

# Allow dependent services a brief moment to be ready
sleep 3

exec /go/bin/reflex -R "^vendor/" -r "\\.go$" -s -- go run ./cmd/wiki-sync
