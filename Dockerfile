# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src

COPY . .

ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/mediawiki.buildVersion=${APP_VERSION} -s -w" -o /out/vrcwiki-connector ./cmd/vrcwiki-connector

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=build /out/vrcwiki-connector /app/vrcwiki-connector
USER nonroot:nonroot
ENTRYPOINT ["/app/vrcwiki-connector"]


FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=build /out/vrcwiki-connector /app/vrcwiki-connector
USER nonroot:nonroot
ENTRYPOINT ["/app/vrcwiki-connector"]

