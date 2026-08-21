package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
	mw "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/mediawiki"
)

func TestQueuePackageEventDedupesNames(t *testing.T) {
	pending := map[string]struct{}{}
	if !queuePackageEvent(pending, sseEvent{Event: "package.updated", Data: "com.foo"}) {
		t.Fatal("expected queued")
	}
	if !queuePackageEvent(pending, sseEvent{Event: "package.added", Data: " com.foo "}) {
		t.Fatal("expected queued")
	}
	if queuePackageEvent(pending, sseEvent{Event: "listing.updated", Data: "com.foo"}) {
		t.Fatal("did not expect non-package event")
	}
	names := takePending(pending)
	if len(names) != 1 || names[0] != "com.foo" {
		t.Fatalf("takePending = %v, want [com.foo]", names)
	}
	if len(pending) != 0 {
		t.Fatalf("pending not cleared: %v", pending)
	}
}

func TestIncrementalSyncSkipsNamesNotOnWiki(t *testing.T) {
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"packages": {
				"com.onwiki": {"versions": {"1.0.0": {"name":"com.onwiki","version":"1.0.0","displayName":"On"}}},
				"com.offwiki": {"versions": {"1.0.0": {"name":"com.offwiki","version":"1.0.0","displayName":"Off"}}}
			}
		}`)
	}))
	t.Cleanup(index.Close)

	wikiCalls := 0
	wiki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Form.Get("action") == "login":
			_, _ = io.WriteString(w, `{"login":{"result":"Success"}}`)
		case r.Form.Get("meta") == "tokens":
			typ := r.Form.Get("type")
			if typ == "" {
				typ = "csrf"
			}
			_, _ = io.WriteString(w, `{"query":{"tokens":{"`+typ+`token":"t"}}}`)
		default:
			wikiCalls++
			_, _ = io.WriteString(w, `{"error":{"code":"unexpected","info":"incremental skip should not call wiki"}}`)
		}
	}))
	t.Cleanup(wiki.Close)

	wikiClient, err := mw.NewMediaWikiClient(mw.WikiConfig{
		URL:      wiki.URL,
		Username: "bot",
		Password: "pw",
	}, wiki.Client())
	if err != nil {
		t.Fatalf("wiki client: %v", err)
	}

	cli, err := apiclient.NewClientWithResponses(index.URL, apiclient.WithHTTPClient(index.Client()))
	if err != nil {
		t.Fatalf("api client: %v", err)
	}

	logger := log.New(io.Discard, "", 0)
	cache := wikiCache{}
	runIncrementalSync(context.Background(), cli, wikiClient, logger, []string{"com.offwiki", "com.onwiki"}, &cache)
	if wikiCalls != 0 {
		t.Fatalf("empty cache incremental sync made %d wiki calls", wikiCalls)
	}
}

type wikiFixture struct {
	index      *httptest.Server
	wiki       *httptest.Server
	pages      map[string]string
	titleQuery []string
	wikiClient *mw.MediaWikiClient
	apiClient  *apiclient.ClientWithResponses
}

func newWikiFixture(t *testing.T) *wikiFixture {
	t.Helper()
	f := &wikiFixture{
		pages: map[string]string{
			"Template:VPM/com.onwiki/Latest_version":             "1.0.0",
			"Template:VPM/com.onwiki/Latest_version/Description": "desc",
			"Template:VPM/com.onwiki/Latest_version/DisplayName": "On",
			"Template:VPM/com.onwiki/Latest_version/License":     "MIT",
		},
	}
	f.index = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			http.NotFound(w, r)
			return
		}
		pkgs := map[string]any{}
		pkgs["com.onwiki"] = map[string]any{
			"versions": map[string]any{"1.0.0": map[string]any{"name": "com.onwiki", "version": "1.0.0", "displayName": "On", "description": "desc", "license": "MIT"}},
		}
		for i := 0; i < 15; i++ {
			name := "com.offwiki.pkg" + strconv.Itoa(i)
			pkgs[name] = map[string]any{
				"versions": map[string]any{"1.0.0": map[string]any{"name": name, "version": "1.0.0", "displayName": name}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"packages": pkgs})
	}))
	t.Cleanup(f.index.Close)

	f.wiki = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.Form.Get("action")
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "login":
			_, _ = io.WriteString(w, `{"login":{"result":"Success"}}`)
		case "query":
			if r.Form.Get("meta") == "tokens" {
				typ := r.Form.Get("type")
				if typ == "" {
					typ = "csrf"
				}
				_, _ = io.WriteString(w, `{"query":{"tokens":{"`+typ+`token":"t"}}}`)
				return
			}
			if r.Form.Get("list") == "allpages" {
				var allpages []map[string]string
				for title := range f.pages {
					allpages = append(allpages, map[string]string{"title": title})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"allpages": allpages}})
				return
			}
			title := r.Form.Get("titles")
			f.titleQuery = append(f.titleQuery, title)
			if content, ok := f.pages[title]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"query": map[string]any{"pages": map[string]any{"1": map[string]any{
						"title":     title,
						"revisions": []any{map[string]any{"slots": map[string]any{"main": map[string]any{"*": content}}}},
					}}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"query": map[string]any{"pages": map[string]any{"-1": map[string]any{"title": title, "missing": ""}}},
			})
		case "edit":
			f.pages[r.Form.Get("title")] = r.Form.Get("text")
			_, _ = io.WriteString(w, `{"edit":{"result":"Success"}}`)
		default:
			_, _ = io.WriteString(w, `{"error":{"code":"unknown","info":"`+action+`"}}`)
		}
	}))
	t.Cleanup(f.wiki.Close)

	var err error
	f.wikiClient, err = mw.NewMediaWikiClient(mw.WikiConfig{
		URL:      f.wiki.URL,
		Username: "bot",
		Password: "pw",
	}, f.wiki.Client())
	if err != nil {
		t.Fatalf("wiki client: %v", err)
	}
	f.apiClient, err = apiclient.NewClientWithResponses(f.index.URL, apiclient.WithHTTPClient(f.index.Client()))
	if err != nil {
		t.Fatalf("api client: %v", err)
	}
	return f
}

func TestSummaryRefreshDoesNotProbeLatestVersionPages(t *testing.T) {
	f := newWikiFixture(t)
	f.titleQuery = nil
	logger := log.New(io.Discard, "", 0)
	cache := runSummaryRefresh(context.Background(), f.apiClient, f.wikiClient, logger, wikiCache{})
	if _, ok := cache.packagePages["com.onwiki"]; !ok {
		t.Fatalf("cache missing on-wiki package: %v", cache.packagePages)
	}

	for _, title := range f.titleQuery {
		if strings.Contains(title, "com.offwiki") {
			t.Fatalf("summary refresh probed off-wiki title %q", title)
		}
		if strings.Contains(title, "Latest_version") || strings.Contains(title, "Latest version") {
			t.Fatalf("summary refresh content-probed template %q", title)
		}
	}
	if len(f.titleQuery) != 1 || f.titleQuery[0] != mw.VersionSummaryPageTitle {
		t.Fatalf("title queries = %v, want only %q", f.titleQuery, mw.VersionSummaryPageTitle)
	}
}

func TestSummaryRefreshEnablesLaterIncremental(t *testing.T) {
	f := newWikiFixture(t)
	logger := log.New(io.Discard, "", 0)
	cache := runSummaryRefresh(context.Background(), f.apiClient, f.wikiClient, logger, wikiCache{})
	f.titleQuery = nil

	runIncrementalSync(context.Background(), f.apiClient, f.wikiClient, logger, []string{"com.onwiki"}, &cache)

	sawLatest := false
	for _, title := range f.titleQuery {
		if strings.Contains(title, "com.offwiki") {
			t.Fatalf("incremental probed off-wiki title %q", title)
		}
		if title == "Template:VPM/com.onwiki/Latest_version" {
			sawLatest = true
		}
	}
	if !sawLatest {
		t.Fatalf("expected Latest_version query after cache was filled, got %v", f.titleQuery)
	}
}
