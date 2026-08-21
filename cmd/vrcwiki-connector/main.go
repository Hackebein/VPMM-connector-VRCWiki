package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
	mw "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/mediawiki"
)

const (
	incrementalSyncDelay       = 30 * time.Second
	summaryRefreshInterval     = 6 * time.Hour
	initialSummaryRefreshDelay = 30 * time.Second
)

// minimal SSE event
type sseEvent struct {
	Event string
	Data  string
}

type wikiCache struct {
	packagePages map[string][]string
	wikiVersions map[string][]string
	titles       map[string]struct{}
}

func main() {
	logger := log.New(os.Stdout, "vrcwiki-connector ", log.LstdFlags)

	vpmmBaseURL := "http://api:8080"
	sseURL := strings.TrimRight(vpmmBaseURL, "/") + "/sse"

	wikiAPI := os.Getenv("VRCWIKI_API_URL")
	wikiUser := os.Getenv("VRCWIKI_USERNAME")
	wikiPass := os.Getenv("VRCWIKI_PASSWORD")
	wikiHdrName := os.Getenv("VRCWIKI_AUTHORIZATION_HEADER")
	wikiHdrValue := os.Getenv("VRCWIKI_AUTHORIZATION_VALUE")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpClient := &http.Client{Timeout: 60 * time.Second}
	sseClient := &http.Client{Timeout: 0 * time.Second}

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

	incrTimer := time.NewTimer(incrementalSyncDelay)
	stopTimer(incrTimer)

	summaryTimer := time.NewTimer(initialSummaryRefreshDelay)
	defer incrTimer.Stop()
	defer summaryTimer.Stop()

	cli, err := apiclient.NewClientWithResponses(vpmmBaseURL, apiclient.WithHTTPClient(httpClient))
	if err != nil {
		logger.Fatalf("init api client: %v", err)
	}

	events := make(chan sseEvent, 8)
	var lastID string
	go func() {
		defer close(events)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			if err := apiclient.ListenSSE(ctx, sseURL, sseClient, &lastID, apiclient.SSEHandlers{
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
			time.Sleep(1 * time.Second)
		}
	}()

	pending := map[string]struct{}{}
	var cache wikiCache

	for {
		select {
		case <-ctx.Done():
			logger.Println("shutting down")
			return
		case ev, ok := <-events:
			if !ok {
				continue
			}
			if queuePackageEvent(pending, ev) {
				resetTimer(incrTimer, incrementalSyncDelay)
			}
		case <-incrTimer.C:
			names := takePending(pending)
			if len(names) == 0 {
				continue
			}
			logger.Printf("running wiki incremental sync: packages=%d", len(names))
			runIncrementalSync(ctx, cli, wikiClient, logger, names, &cache)
		case <-summaryTimer.C:
			logger.Println("running wiki summary refresh")
			cache = runSummaryRefresh(ctx, cli, wikiClient, logger, cache)
			summaryTimer.Reset(summaryRefreshInterval)
		}
	}
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	stopTimer(t)
	t.Reset(d)
}

func queuePackageEvent(pending map[string]struct{}, ev sseEvent) bool {
	switch ev.Event {
	case "package.added", "package.updated", "package.removed":
		name := strings.TrimSpace(ev.Data)
		if name == "" {
			return false
		}
		pending[name] = struct{}{}
		return true
	default:
		return false
	}
}

func takePending(pending map[string]struct{}) []string {
	names := make([]string, 0, len(pending))
	for name := range pending {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		delete(pending, name)
	}
	return names
}

func wikiPackagesOnCache(names []string, cache wikiCache) (onWiki []string, skipped int) {
	for _, name := range names {
		if _, ok := cache.packagePages[name]; ok {
			onWiki = append(onWiki, name)
			continue
		}
		skipped++
	}
	return onWiki, skipped
}

func logWikiStats(logger *log.Logger, kind string, extra string, delta mw.RequestStats) {
	logger.Printf("wiki %s complete: %squeries=%d edits=%d skips=%d deletes=%d requests=%d",
		kind, extra, delta.Queries, delta.Edits, delta.Skips, delta.Deletes, delta.Requests)
}

func runIncrementalSync(ctx context.Context, cli *apiclient.ClientWithResponses, wikiClient *mw.MediaWikiClient, logger *log.Logger, names []string, cache *wikiCache) {
	if cache == nil {
		logWikiStats(logger, "incremental sync", fmtExtra(len(names), 0, len(names)), mw.RequestStats{})
		return
	}
	onWiki, skipped := wikiPackagesOnCache(names, *cache)
	if len(onWiki) == 0 {
		logWikiStats(logger, "incremental sync", fmtExtra(len(names), 0, skipped), mw.RequestStats{})
		return
	}

	before := wikiClient.Stats()
	pkgs, ok := loadIndex(ctx, cli, logger, "incremental sync")
	if !ok {
		return
	}
	allVersionsMap := mw.BuildAllVersionsMapFromAPI(pkgs)
	latestMap, stableMap, unstableMap := mw.ComputeLatestStableUnstable(allVersionsMap)
	if err := wikiClient.SyncNamedPackages(onWiki, latestMap, stableMap, unstableMap, allVersionsMap, cache.titles, cache.wikiVersions); err != nil {
		logger.Printf("incremental sync: %v", err)
	}
	*cache = refreshTitlesAndSummary(wikiClient, logger, "incremental sync", pkgs, *cache)
	logWikiStats(logger, "incremental sync", fmtExtra(len(names), len(onWiki), skipped), wikiClient.Stats().Sub(before))
}

func fmtExtra(queued, onWiki, skipped int) string {
	return "queued=" + strconv.Itoa(queued) + " wiki_packages=" + strconv.Itoa(onWiki) + " skipped=" + strconv.Itoa(skipped) + " "
}

func runSummaryRefresh(ctx context.Context, cli *apiclient.ClientWithResponses, wikiClient *mw.MediaWikiClient, logger *log.Logger, prev wikiCache) wikiCache {
	before := wikiClient.Stats()
	pkgs, ok := loadIndex(ctx, cli, logger, "summary refresh")
	if !ok {
		return prev
	}
	cache := refreshTitlesAndSummary(wikiClient, logger, "summary refresh", pkgs, prev)
	logWikiStats(logger, "summary refresh", "wiki_packages="+strconv.Itoa(len(cache.packagePages))+" ", wikiClient.Stats().Sub(before))
	return cache
}

func refreshTitlesAndSummary(wikiClient *mw.MediaWikiClient, logger *log.Logger, logPrefix string, pkgs []apiclient.Package, prev wikiCache) wikiCache {
	allVersionsMap := mw.BuildAllVersionsMapFromAPI(pkgs)
	packagePages, wikiVersionsMap, err := wikiClient.ScanVpmPages()
	if err != nil {
		logger.Printf("%s: scan wiki: %v", logPrefix, err)
		return prev
	}
	titles := mw.PageTitleSet(packagePages)
	cache := wikiCache{packagePages: packagePages, wikiVersions: wikiVersionsMap, titles: titles}
	table, err := mw.GenerateVersionSummaryWikiTableWithWikiVersions(wikiVersionsMap, allVersionsMap)
	if err != nil {
		logger.Printf("%s: generate version table: %v", logPrefix, err)
		return cache
	}
	if err := wikiClient.EditPage(mw.VersionSummaryPageTitle, table, true); err != nil {
		logger.Printf("%s: update version summary page: %v", logPrefix, err)
	}
	return cache
}

func loadIndex(ctx context.Context, cli *apiclient.ClientWithResponses, logger *log.Logger, logPrefix string) ([]apiclient.Package, bool) {
	resp, err := cli.GetIndexWithResponse(ctx, nil)
	if err != nil {
		logger.Printf("%s: get index: %v", logPrefix, err)
		return nil, false
	}

	if resp.StatusCode() != http.StatusOK {
		switch {
		case resp.ApplicationproblemJSON401 != nil:
			logger.Printf("%s: get index: unauthorized: %s", logPrefix, safeErrDetail(resp.ApplicationproblemJSON401))
		case resp.ApplicationproblemJSON422 != nil:
			logger.Printf("%s: get index: unprocessable: %s", logPrefix, safeErrDetail(resp.ApplicationproblemJSON422))
		case resp.ApplicationproblemJSON500 != nil:
			logger.Printf("%s: get index: server error: %s", logPrefix, safeErrDetail(resp.ApplicationproblemJSON500))
		default:
			logger.Printf("%s: get index: unexpected status: %s", logPrefix, resp.Status())
		}
		return nil, false
	}
	if len(resp.Body) == 0 {
		logger.Printf("%s: get index: empty response body", logPrefix)
		return nil, false
	}

	var idx vccIndex
	if err := json.Unmarshal(resp.Body, &idx); err != nil {
		logger.Printf("%s: get index: decode json: %v", logPrefix, err)
		return nil, false
	}
	return flattenIndexPackages(&idx), true
}

// vccIndex is the structure of `/index.json` (VPM/VCC spec listing).
type vccIndex struct {
	Packages map[string]vccIndexPackage `json:"packages"`
}

type vccIndexPackage struct {
	Versions map[string]mw.IndexPackageVersion `json:"versions"`
}

// flattenIndexPackages converts an index response into a slice of packages sorted
// by version descending per package so downstream helpers continue to see the
// latest version first.
func flattenIndexPackages(idx *vccIndex) []apiclient.Package {
	if idx == nil || len(idx.Packages) == 0 {
		return nil
	}

	names := make([]string, 0, len(idx.Packages))
	for name := range idx.Packages {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []apiclient.Package
	for _, name := range names {
		listPkg := idx.Packages[name]
		if len(listPkg.Versions) == 0 {
			continue
		}
		versions := make([]apiclient.Package, 0, len(listPkg.Versions))
		for versionKey, entry := range listPkg.Versions {
			entry.VersionKey = versionKey
			versions = append(versions, mw.PackageFromIndexVersion(name, entry))
		}
		sortPackagesByVersionDesc(versions)
		out = append(out, versions...)
	}
	return out
}

func sortPackagesByVersionDesc(pkgs []apiclient.Package) {
	sort.SliceStable(pkgs, func(i, j int) bool {
		left := strings.TrimSpace(mw.PackageVersion(pkgs[i]))
		right := strings.TrimSpace(mw.PackageVersion(pkgs[j]))
		vi, errI := semver.NewVersion(left)
		vj, errJ := semver.NewVersion(right)

		switch {
		case errI == nil && errJ == nil:
			return vi.GreaterThan(vj)
		case errI == nil:
			return true
		case errJ == nil:
			return false
		default:
			return left > right
		}
	})
}

func safeErrDetail(e *apiclient.ErrorModel) string {
	if e == nil {
		return ""
	}
	if e.Title != nil && e.Detail != nil {
		return *e.Title + ": " + *e.Detail
	}
	if e.Detail != nil {
		return *e.Detail
	}
	if e.Title != nil {
		return *e.Title
	}
	return ""
}
