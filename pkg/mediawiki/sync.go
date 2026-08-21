package mediawiki

import (
	"fmt"
	"sort"
	"strings"

	apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
)

// canonWikiTitle maps MediaWiki title spaces and underscores to a single form.
func canonWikiTitle(title string) string {
	return strings.ReplaceAll(title, " ", "_")
}

// PageTitleSet builds an existence index from ScanVpmPages package page lists.
func PageTitleSet(packagePages map[string][]string) map[string]struct{} {
	n := 0
	for _, pages := range packagePages {
		n += len(pages)
	}
	set := make(map[string]struct{}, n)
	for _, pages := range packagePages {
		for _, p := range pages {
			set[canonWikiTitle(p)] = struct{}{}
		}
	}
	return set
}

func hasWikiTitle(titles map[string]struct{}, title string) bool {
	if len(titles) == 0 {
		return false
	}
	_, ok := titles[canonWikiTitle(title)]
	return ok
}

func packageNames(packagePages map[string][]string) []string {
	names := make([]string, 0, len(packagePages))
	for name := range packagePages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func versionsByTag(versions []apiclient.Package) map[string]apiclient.Package {
	known := make(map[string]apiclient.Package, len(versions))
	for _, pv := range versions {
		known[PackageVersion(pv)] = pv
	}
	return known
}

// SyncNamedPackages updates Latest_* and wiki-discovered version pages for the
// given package names. existingTitles is the allpages scan; titles not in that
// set are skipped with no existence GET.
func (c *MediaWikiClient) SyncNamedPackages(
	names []string,
	latest map[string]apiclient.Package,
	stable map[string]apiclient.Package,
	unstable map[string]apiclient.Package,
	allVersions map[string][]apiclient.Package,
	existingTitles map[string]struct{},
	wikiVersions map[string][]string,
) error {
	if existingTitles == nil {
		existingTitles = map[string]struct{}{}
	}
	var errs []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if v, ok := latest[name]; ok {
			if err := c.updateLatestPages(v, "Latest_version", existingTitles); err != nil {
				errs = append(errs, fmt.Sprintf("latest %s: %v", name, err))
			}
		}
		if v, ok := stable[name]; ok {
			if err := c.updateLatestPages(v, "Latest_stable_version", existingTitles); err != nil {
				errs = append(errs, fmt.Sprintf("stable %s: %v", name, err))
			}
		}
		if v, ok := unstable[name]; ok {
			if err := c.updateLatestPages(v, "Latest_unstable_version", existingTitles); err != nil {
				errs = append(errs, fmt.Sprintf("unstable %s: %v", name, err))
			}
		}
		known := versionsByTag(allVersions[name])
		for _, tag := range wikiVersions[name] {
			if err := c.processSpecificVersionPage(name, tag, known, existingTitles); err != nil {
				errs = append(errs, fmt.Sprintf("version %s/%s: %v", name, tag, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sync named packages: %d errors:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// SyncExistingPages scans Template:VPM pages and updates only packages that
// already have wiki pages. It does not probe the rest of the API catalog.
func (c *MediaWikiClient) SyncExistingPages(
	latest map[string]apiclient.Package,
	stable map[string]apiclient.Package,
	unstable map[string]apiclient.Package,
	allVersions map[string][]apiclient.Package,
) error {
	packagePages, wikiVersionsMap, err := c.ScanVpmPages()
	if err != nil {
		return err
	}
	return c.SyncNamedPackages(
		packageNames(packagePages),
		latest,
		stable,
		unstable,
		allVersions,
		PageTitleSet(packagePages),
		wikiVersionsMap,
	)
}
