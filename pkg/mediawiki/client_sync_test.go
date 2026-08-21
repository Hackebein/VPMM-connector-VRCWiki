package mediawiki

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
)

type wikiTransport struct {
	mu    sync.Mutex
	pages map[string]string
	calls []url.Values
}

func newWikiTransport(pages map[string]string) *wikiTransport {
	cloned := make(map[string]string, len(pages))
	for k, v := range pages {
		cloned[k] = v
	}
	return &wikiTransport{pages: cloned}
}

func (t *wikiTransport) lookup(title string) (string, bool) {
	if v, ok := t.pages[title]; ok {
		return v, true
	}
	if v, ok := t.pages[strings.ReplaceAll(title, "_", " ")]; ok {
		return v, true
	}
	if v, ok := t.pages[strings.ReplaceAll(title, " ", "_")]; ok {
		return v, true
	}
	return "", false
}

func jsonResp(v any) *http.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func (t *wikiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, vals)

	switch vals.Get("action") {
	case "login":
		return jsonResp(map[string]any{"login": map[string]any{"result": "Success"}}), nil
	case "query":
		if vals.Get("meta") == "tokens" {
			tokenType := vals.Get("type")
			if tokenType == "" {
				tokenType = "csrf"
			}
			return jsonResp(map[string]any{
				"query": map[string]any{
					"tokens": map[string]any{tokenType + "token": "TESTTOKEN"},
				},
			}), nil
		}
		if vals.Get("list") == "allpages" {
			prefix := vals.Get("apprefix")
			var allpages []map[string]any
			for title := range t.pages {
				rest := strings.TrimPrefix(title, "Template:")
				if strings.HasPrefix(rest, prefix) || strings.HasPrefix(title, prefix) {
					allpages = append(allpages, map[string]any{"title": title})
				}
			}
			return jsonResp(map[string]any{"query": map[string]any{"allpages": allpages}}), nil
		}
		title := vals.Get("titles")
		if title == "" {
			return jsonResp(map[string]any{"error": map[string]any{"code": "badquery", "info": "missing titles"}}), nil
		}
		if content, ok := t.lookup(title); ok {
			return jsonResp(map[string]any{
				"query": map[string]any{
					"pages": map[string]any{
						"1": map[string]any{
							"title": title,
							"revisions": []any{
								map[string]any{
									"slots": map[string]any{
										"main": map[string]any{"*": content},
									},
								},
							},
						},
					},
				},
			}), nil
		}
		return jsonResp(map[string]any{
			"query": map[string]any{
				"pages": map[string]any{
					"-1": map[string]any{"title": title, "missing": ""},
				},
			},
		}), nil
	case "edit":
		title := vals.Get("title")
		t.pages[title] = vals.Get("text")
		return jsonResp(map[string]any{"edit": map[string]any{"result": "Success"}}), nil
	case "delete":
		delete(t.pages, vals.Get("title"))
		return jsonResp(map[string]any{"delete": map[string]any{"title": vals.Get("title")}}), nil
	default:
		return jsonResp(map[string]any{"error": map[string]any{"code": "unknown", "info": vals.Get("action")}}), nil
	}
}

func (t *wikiTransport) snapshot() []url.Values {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]url.Values, len(t.calls))
	copy(out, t.calls)
	return out
}

func (t *wikiTransport) resetCalls() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = nil
}

func titleQueries(calls []url.Values) []string {
	var titles []string
	for _, c := range calls {
		if c.Get("action") == "query" && c.Get("titles") != "" {
			titles = append(titles, c.Get("titles"))
		}
	}
	return titles
}

func editCount(calls []url.Values) int {
	n := 0
	for _, c := range calls {
		if c.Get("action") == "edit" {
			n++
		}
	}
	return n
}

func queriedPackageNames(calls []url.Values) map[string]int {
	out := map[string]int{}
	for _, title := range titleQueries(calls) {
		rest, ok := strings.CutPrefix(title, "Template:VPM/")
		if !ok {
			continue
		}
		pkg, _, _ := strings.Cut(rest, "/")
		if pkg == "" || pkg == "Version summary" || pkg == "Version_summary" {
			continue
		}
		out[pkg]++
	}
	return out
}

func newTestWikiClient(t *testing.T, tr *wikiTransport) *MediaWikiClient {
	t.Helper()
	cli, err := NewMediaWikiClient(WikiConfig{
		URL:      "https://wiki.test/api.php",
		Username: "bot",
		Password: "secret",
	}, &http.Client{Transport: tr})
	if err != nil {
		t.Fatalf("NewMediaWikiClient: %v", err)
	}
	return cli
}

func testPackage(name, version, display, desc, license, author string) apiclient.Package {
	var d, l *string
	if desc != "" {
		d = &desc
	}
	if license != "" {
		l = &license
	}
	var a *apiclient.VPMAuthor
	if author != "" {
		a = &apiclient.VPMAuthor{Name: &author}
	}
	return PackageFromIndexVersion(name, IndexPackageVersion{
		VersionKey:  version,
		Version:     version,
		DisplayName: display,
		Description: d,
		License:     l,
		Author:      a,
	})
}

func latestTree(name, version, display, desc, license, author string) map[string]string {
	pages := map[string]string{
		fmt.Sprintf("Template:VPM/%s/Latest_version", name):             version,
		fmt.Sprintf("Template:VPM/%s/Latest_version/Description", name): desc,
		fmt.Sprintf("Template:VPM/%s/Latest_version/DisplayName", name): display,
		fmt.Sprintf("Template:VPM/%s/Latest_version/License", name):     license,
	}
	if author != "" {
		pages[fmt.Sprintf("Template:VPM/%s/Latest_version/Author_1", name)] = author
	}
	return pages
}

func catalog(onWiki apiclient.Package, extra int) (latest map[string]apiclient.Package, all map[string][]apiclient.Package) {
	latest = map[string]apiclient.Package{PackageName(onWiki): onWiki}
	all = map[string][]apiclient.Package{PackageName(onWiki): {onWiki}}
	for i := 0; i < extra; i++ {
		name := fmt.Sprintf("com.offwiki.pkg%d", i)
		p := testPackage(name, "1.0.0", name, "d", "MIT", "")
		latest[name] = p
		all[name] = []apiclient.Package{p}
	}
	return latest, all
}

func TestSyncExistingPagesSkipsPackagesNotOnWiki(t *testing.T) {
	on := testPackage("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	tr := newWikiTransport(latestTree("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada"))
	cli := newTestWikiClient(t, tr)
	tr.resetCalls()

	latest, all := catalog(on, 20)
	if err := cli.SyncExistingPages(latest, latest, map[string]apiclient.Package{}, all); err != nil {
		t.Fatalf("SyncExistingPages: %v", err)
	}

	queried := queriedPackageNames(tr.snapshot())
	if queried["com.onwiki"] == 0 {
		t.Fatalf("expected queries for com.onwiki, got %v", queried)
	}
	for name := range queried {
		if strings.HasPrefix(name, "com.offwiki.") {
			t.Fatalf("probed off-wiki package %s: %v", name, queried)
		}
	}
}

func TestKnownTitlesSkipPageExists(t *testing.T) {
	on := testPackage("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	pages := latestTree("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	tr := newWikiTransport(pages)
	cli := newTestWikiClient(t, tr)
	tr.resetCalls()

	latest := map[string]apiclient.Package{"com.onwiki": on}
	all := map[string][]apiclient.Package{"com.onwiki": {on}}
	if err := cli.SyncExistingPages(latest, nil, nil, all); err != nil {
		t.Fatalf("SyncExistingPages: %v", err)
	}

	wantTitle := "Template:VPM/com.onwiki/Latest_version"
	n := 0
	for _, title := range titleQueries(tr.snapshot()) {
		if title == wantTitle {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Latest_version content GETs = %d, want 1 (EditPage only, no pageExists)", n)
	}
}

func TestEditPageSkipsUnchanged(t *testing.T) {
	on := testPackage("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	tr := newWikiTransport(latestTree("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada"))
	cli := newTestWikiClient(t, tr)
	before := cli.Stats()
	tr.resetCalls()

	if err := cli.SyncExistingPages(
		map[string]apiclient.Package{"com.onwiki": on},
		nil,
		nil,
		map[string][]apiclient.Package{"com.onwiki": {on}},
	); err != nil {
		t.Fatalf("SyncExistingPages: %v", err)
	}

	calls := tr.snapshot()
	if got := editCount(calls); got != 0 {
		t.Fatalf("edits = %d, want 0 for unchanged pages", got)
	}
	delta := cli.Stats().Sub(before)
	if delta.Skips == 0 {
		t.Fatal("expected skip-if-unchanged stats")
	}
	if delta.Edits != 0 {
		t.Fatalf("stats edits = %d, want 0", delta.Edits)
	}
}

func TestSyncNamedPackagesOnlyTouchesQueuedName(t *testing.T) {
	on := testPackage("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	other := testPackage("com.otherwiki", "2.0.0", "Other", "d", "MIT", "Bob")
	pages := latestTree("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	for k, v := range latestTree("com.otherwiki", "2.0.0", "Other", "d", "MIT", "Bob") {
		pages[k] = v
	}
	tr := newWikiTransport(pages)
	cli := newTestWikiClient(t, tr)

	packagePages, _, err := cli.ScanVpmPages()
	if err != nil {
		t.Fatalf("ScanVpmPages: %v", err)
	}
	tr.resetCalls()

	latest := map[string]apiclient.Package{"com.onwiki": on, "com.otherwiki": other}
	all := map[string][]apiclient.Package{"com.onwiki": {on}, "com.otherwiki": {other}}
	if err := cli.SyncNamedPackages(
		[]string{"com.onwiki"},
		latest, nil, nil, all,
		PageTitleSet(packagePages),
		map[string][]string{},
	); err != nil {
		t.Fatalf("SyncNamedPackages: %v", err)
	}

	queried := queriedPackageNames(tr.snapshot())
	if queried["com.otherwiki"] != 0 {
		t.Fatalf("incremental sync probed unqueued wiki package: %v", queried)
	}
	if queried["com.onwiki"] == 0 {
		t.Fatalf("expected queries for queued package, got %v", queried)
	}
}

func TestPageTitleSetMatchesSpacesAndUnderscores(t *testing.T) {
	set := PageTitleSet(map[string][]string{
		"com.onwiki": {"Template:VPM/com.onwiki/Latest version"},
	})
	if !hasWikiTitle(set, "Template:VPM/com.onwiki/Latest_version") {
		t.Fatal("expected underscore title to match spaced allpages title")
	}
	if hasWikiTitle(set, "Template:VPM/com.offwiki/Latest_version") {
		t.Fatal("off-wiki title should not be present")
	}
}

func TestAbsentFromTitleSetMakesZeroWikiGETs(t *testing.T) {
	on := testPackage("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada")
	off := testPackage("com.offwiki", "1.0.0", "Off", "d", "MIT", "")
	tr := newWikiTransport(latestTree("com.onwiki", "1.0.0", "On", "desc", "MIT", "Ada"))
	cli := newTestWikiClient(t, tr)
	tr.resetCalls()

	err := cli.SyncNamedPackages(
		[]string{"com.offwiki"},
		map[string]apiclient.Package{"com.offwiki": off, "com.onwiki": on},
		nil, nil,
		map[string][]apiclient.Package{"com.offwiki": {off}, "com.onwiki": {on}},
		PageTitleSet(map[string][]string{"com.onwiki": {"Template:VPM/com.onwiki/Latest_version"}}),
		map[string][]string{},
	)
	if err != nil {
		t.Fatalf("SyncNamedPackages: %v", err)
	}
	if len(titleQueries(tr.snapshot())) != 0 {
		t.Fatalf("expected zero title queries for off-wiki package, got %v", titleQueries(tr.snapshot()))
	}
}
