package apiclient

//go:generate sh -c "curl -fsSL https://vpmm.dev/openapi-3.0.json -o ./openapi.json"
//go:generate sh -c "go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=./oapi.config.yaml ./openapi.json > ./client.gen.go"

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/r3labs/sse/v2"
)

// SSEHandlers contains callbacks for supported server-sent events.
type SSEHandlers struct {
	OnPackageAdded   func(event PackageAddedEvent)
	OnPackageUpdated func(event PackageUpdatedEvent)
	OnPackageRemoved func(event PackageRemovedEvent)

	// Optional generic hook for unhandled events.
	OnUnknown func(name string, raw json.RawMessage)
}

// ListenSSE connects to the SSE endpoint and dispatches events to provided handlers.
func ListenSSE(ctx context.Context, sseURL string, httpClient *http.Client, h SSEHandlers) error {
	client := sse.NewClient(sseURL)
	if httpClient != nil {
		// r3labs/sse v2 uses Connection for custom transports/timeouts
		client.Connection = httpClient
	}
	// Ensure we request the proper stream content type
	if client.Headers == nil {
		client.Headers = make(map[string]string)
	}
	client.Headers["Accept"] = "text/event-stream"

	// Use context-aware subscription; empty channel subscribes to default stream
	return client.SubscribeWithContext(ctx, "", func(msg *sse.Event) {
		name := string(msg.Event)
		if len(msg.Data) == 0 {
			return
		}
		switch name {
		case "package.added":
			var ev PackageAddedEvent
			if err := json.Unmarshal(msg.Data, &ev); err == nil && h.OnPackageAdded != nil {
				h.OnPackageAdded(ev)
			}
		case "package.updated":
			var ev PackageUpdatedEvent
			if err := json.Unmarshal(msg.Data, &ev); err == nil && h.OnPackageUpdated != nil {
				h.OnPackageUpdated(ev)
			}
		case "package.removed":
			var ev PackageRemovedEvent
			if err := json.Unmarshal(msg.Data, &ev); err == nil && h.OnPackageRemoved != nil {
				h.OnPackageRemoved(ev)
			}
		default:
			if h.OnUnknown != nil {
				h.OnUnknown(name, json.RawMessage(msg.Data))
			}
		}
	})
}
