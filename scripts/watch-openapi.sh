#!/usr/bin/env bash
set -euo pipefail

# Default to production spec; override via OPENAPI_URL
URL="${OPENAPI_URL:-https://vpmm.dev/openapi-3.0.json}"

# Work from the apiclient directory
cd "$(dirname "$0")/../pkg/apiclient"

prev_hash=""
if [ -f "openapi.json" ]; then
  prev_hash=$(sha256sum openapi.json | awk '{print $1}')
fi

# Ensure oapi-codegen CLI is available without modifying go.mod
if [ ! -x "/go/bin/oapi-codegen" ]; then
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest || true
fi

while true; do
  if curl -fsS "$URL" -o openapi.json; then
    new_hash=$(sha256sum openapi.json | awk '{print $1}')
    if [ "$new_hash" != "$prev_hash" ]; then
      prev_hash="$new_hash"
      /go/bin/oapi-codegen --config ./oapi.config.yaml ./openapi.json > ./client.gen.go || true
      # Optional: compile check (non-fatal)
      go build -o /dev/null ./... || true
      sleep 20
    fi
  fi
  sleep 10
done


