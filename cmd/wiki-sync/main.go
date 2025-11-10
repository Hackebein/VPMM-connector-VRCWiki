package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hackebein/vpmm/apps/wiki-sync/pkg/apiclient"
	mw "github.com/hackebein/vpmm/apps/wiki-sync/pkg/mediawiki"
)

func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

// minimal SSE event
type sseEvent struct {
	Event string
	Data  string
}

func main() {
	logger := log.New(os.Stdout, "wiki-sync ", log.LstdFlags)

	vpmmBaseURL := getenv("VPMM_API_BASE_URL", "https://vpmm.dev")
	sseURL := strings.TrimRight(vpmmBaseURL, "/") + "/sse"

	wikiAPI := os.Getenv("VRCWIKI_API_URL")
	wikiUser := os.Getenv("VRCWIKI_USERNAME")
	wikiPass := os.Getenv("VRCWIKI_PASSWORD")
	wikiHdrName := os.Getenv("VRCWIKI_AUTHORIZATION_HEADER")
	wikiHdrValue := os.Getenv("VRCWIKI_AUTHORIZATION_VALUE")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpClient := &http.Client{Timeout: 60 * time.Second}

	wikiClient, err := mw.NewMediaWikiClient(mw.WikiConfig{
		URL:       wikiAPI,
		Username:  wikiUser,
		Password:  wikiPass,
		Header:    wikiHdrName,
		HeaderVal: wikiHdrValue,
	}, httpClient)
	if err != nil {
		logger.Fatalf("init wiki client: %v", err)
	}

	// debouncer for full syncs
	syncDelay := 30 * time.Second
	syncTimer := time.NewTimer(syncDelay)
	// initial sync after delay
	resetTimer := func() {
		if !syncTimer.Stop() {
			select {
			case <-syncTimer.C:
			default:
			}
		}
		syncTimer.Reset(syncDelay)
	}
	resetTimer()

	// initialize generated API client
	cli, err := apiclient.NewClientWithResponses(vpmmBaseURL, apiclient.WithHTTPClient(httpClient))
	if err != nil {
		logger.Fatalf("init api client: %v", err)
	}

	// SSE loop with backoff
	events := make(chan sseEvent, 8)
	var lastID string
	go func() {
		defer close(events)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			if err := apiclient.ListenSSE(ctx, sseURL, httpClient, &lastID, apiclient.SSEHandlers{
				OnPackageAdded: func(event apiclient.PackageAddedEvent) {
					events <- sseEvent{Event: "package.added", Data: event.Identifier.Name}
				},
				OnPackageUpdated: func(event apiclient.PackageUpdatedEvent) {
					events <- sseEvent{Event: "package.updated", Data: event.Identifier.Name}
				},
				OnPackageRemoved: func(event apiclient.PackageRemovedEvent) {
					events <- sseEvent{Event: "package.removed", Data: event.Identifier.Name}
				},
			}); err != nil {
				logger.Printf("sse error: %v", err)
				time.Sleep(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			// normal end or server close, short pause then reconnect
			time.Sleep(1 * time.Second)
		}
	}()

	// main loop: debounce triggers
	for {
		select {
		case <-ctx.Done():
			logger.Println("shutting down")
			return
		case ev, ok := <-events:
			if !ok {
				// channel closed; terminate gracefully allowing any pending sync
				continue
			}
			switch ev.Event {
			case "package.added", "package.updated", "package.removed":
				resetTimer()
			}
		case <-syncTimer.C:
			// execute full sync
			logger.Println("running wiki full sync")
			runFullSync(ctx, cli, wikiClient, logger)
		}
	}
}

// runFullSync orchestrates a complete wiki sync using the new client helpers.
func runFullSync(ctx context.Context, cli *apiclient.ClientWithResponses, wikiClient *mw.MediaWikiClient, logger *log.Logger) {
	resp, err := cli.ListPackagesWithResponse(ctx, nil)
	if err != nil {
		logger.Printf("full sync: list packages: %v", err)
		return
	}
	if resp.JSON200 == nil {
		logger.Printf("full sync: empty response")
		return
	}
	pkgs := *resp.JSON200

	// Build versions map and compute latest/stable/unstable
	allVersionsMap := mw.BuildAllVersionsMapFromAPI(pkgs)
	latestMap, stableMap, unstableMap := mw.ComputeLatestStableUnstable(allVersionsMap)

	// Scan wiki
	packagePages, wikiVersionsMap, err := wikiClient.ScanVpmPages()
	if err != nil {
		logger.Printf("full sync: scan wiki: %v", err)
		// continue with what we have
		packagePages = map[string][]string{}
		wikiVersionsMap = map[string][]string{}
	}

	// Union of package names from API and wiki
	nameSet := make(map[string]struct{})
	for name := range allVersionsMap {
		nameSet[name] = struct{}{}
	}
	for name := range packagePages {
		nameSet[name] = struct{}{}
	}

	// For each package, update latest/stable/unstable and specific versions
	for name := range nameSet {
		if v, ok := latestMap[name]; ok {
			if err := wikiClient.UpdateLatestVersionPages(v); err != nil {
				logger.Printf("full sync: update latest for %s: %v", name, err)
			}
		}
		if v, ok := stableMap[name]; ok {
			if err := wikiClient.UpdateLatestStableVersionPages(v); err != nil {
				logger.Printf("full sync: update latest stable for %s: %v", name, err)
			}
		}
		if v, ok := unstableMap[name]; ok {
			if err := wikiClient.UpdateLatestUnstableVersionPages(v); err != nil {
				logger.Printf("full sync: update latest unstable for %s: %v", name, err)
			}
		}

		// known versions for this package
		known := make(map[string]apiclient.Package)
		if vs, ok := allVersionsMap[name]; ok {
			for _, pv := range vs {
				known[pv.Version] = pv
			}
		}
		// process version pages detected on wiki
		if versions, ok := wikiVersionsMap[name]; ok {
			for _, tag := range versions {
				if err := wikiClient.ProcessSpecificVersionPage(name, tag, known); err != nil {
					logger.Printf("full sync: process version %s/%s: %v", name, tag, err)
				}
			}
		}
	}

	// Generate and write the version summary table
	table, err := mw.GenerateVersionSummaryWikiTableWithWikiVersions(wikiVersionsMap, allVersionsMap)
	if err != nil {
		logger.Printf("full sync: generate version table: %v", err)
		return
	}
	if err := wikiClient.EditPage("Template:VPM/Version summary", table, true); err != nil {
		logger.Printf("full sync: update version summary page: %v", err)
	}
}
