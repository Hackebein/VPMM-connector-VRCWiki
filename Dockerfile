# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build
RUN apk add --no-cache curl git ca-certificates build-base
WORKDIR /src

# Pre-install generator tool
RUN go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1

# Copy sources
COPY . .

# Generate API client
RUN go generate ./pkg/apiclient

# Build binary
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/vrcwiki-connector ./cmd/vrcwiki-connector

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=build /out/vrcwiki-connector /app/vrcwiki-connector
USER nonroot:nonroot
ENTRYPOINT ["/app/vrcwiki-connector"]

