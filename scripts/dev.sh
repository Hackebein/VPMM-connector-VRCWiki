#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends curl ca-certificates
update-ca-certificates

export PATH="/usr/local/go/bin:${PATH}"
export PATH="/go/bin:${PATH}"

# Ensure required tools are available
if ! command -v oapi-codegen >/dev/null 2>&1; then
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1
fi

if ! command -v reflex >/dev/null 2>&1; then
  go install github.com/cespare/reflex@latest
fi

# Generate API client (uses //go:generate in pkg/apiclient)
go generate ./pkg/apiclient

# Wait for the OpenAPI endpoint to be reachable, then keep client in sync
until curl -fsS "${OPENAPI_URL:-https://vpmm.dev/openapi-3.0.json}" -o /dev/null 2>/dev/null; do sleep 1; done
( bash ./scripts/watch-openapi.sh & )

# Allow dependent services a brief moment to be ready
sleep 3

exec /go/bin/reflex -R "^vendor/" -r "\\.go$" -s -- go run ./cmd/vrcwiki-connector
