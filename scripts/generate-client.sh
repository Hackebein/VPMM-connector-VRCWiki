#!/usr/bin/env sh
set -eu
OPENAPI_URL="${OPENAPI_URL:-https://vpmm.dev/openapi-3.0.json}"

echo "Downloading OpenAPI schema from ${OPENAPI_URL}"
curl -fsSL "${OPENAPI_URL}" -o ./openapi.json

if ! command -v oapi-codegen >/dev/null 2>&1; then
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1
fi

echo "Generating Go client from schema"
oapi-codegen --config ./oapi.config.yaml ./openapi.json > ./client.gen.go
