# syntax=docker/dockerfile:1.7

FROM golang:1.22-alpine AS build
RUN apk add --no-cache curl git ca-certificates build-base
WORKDIR /src

# Pre-install generator tool
RUN go install github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1

# Copy sources
COPY . .

# Generate API client
RUN go generate ./pkg/apiclient

# Build binary
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/wiki-sync ./cmd/wiki-sync

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=build /out/wiki-sync /app/wiki-sync
USER nonroot:nonroot
ENTRYPOINT ["/app/wiki-sync"]

