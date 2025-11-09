package mediawiki

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	apiclient "github.com/hackebein/vpmm/apps/wiki-sync/pkg/apiclient"
)

// PackageVersionSummary aggregates latest, stable, unstable and known wiki versions for a package.
type PackageVersionSummary struct {
	Name           string
	DisplayName    string
	LatestVersion  *apiclient.Package
	LatestStable   *apiclient.Package
	LatestUnstable *apiclient.Package
	WikiVersions   []string
}

// ComputeLatestStableUnstable computes latest, stable-only, and unstable-only maps from all versions per package.
func ComputeLatestStableUnstable(allVersions map[string][]apiclient.Package) (map[string]apiclient.Package, map[string]apiclient.Package, map[string]apiclient.Package) {
	latest := make(map[string]apiclient.Package)
	stable := make(map[string]apiclient.Package)
	unstable := make(map[string]apiclient.Package)

	for pkg, versions := range allVersions {
		var bestLatest *semver.Version
		var bestLatestPV apiclient.Package

		var bestStable *semver.Version
		var bestStablePV apiclient.Package

		var bestUnstable *semver.Version
		var bestUnstablePV apiclient.Package

		for _, v := range versions {
			sv, err := semver.NewVersion(strings.TrimSpace(v.Version))
			if err != nil {
				continue
			}
			// latest
			if bestLatest == nil || sv.GreaterThan(bestLatest) {
				cp := v
				bestLatest = sv
				bestLatestPV = cp
			}
			// stable
			if sv.Prerelease() == "" {
				if bestStable == nil || sv.GreaterThan(bestStable) {
					cp := v
					bestStable = sv
					bestStablePV = cp
				}
			} else {
				// unstable
				if bestUnstable == nil || sv.GreaterThan(bestUnstable) {
					cp := v
					bestUnstable = sv
					bestUnstablePV = cp
				}
			}
		}
		if bestLatest != nil {
			latest[pkg] = bestLatestPV
		}
		if bestStable != nil {
			stable[pkg] = bestStablePV
		}
		if bestUnstable != nil {
			unstable[pkg] = bestUnstablePV
		}
	}
	return latest, stable, unstable
}

// GetVersionSummaryTableWithWikiVersions returns a table with latest, stable, unstable, and wiki versions for all packages.
func GetVersionSummaryTableWithWikiVersions(wikiVersionsMap map[string][]string, allVersionsMap map[string][]apiclient.Package) ([]PackageVersionSummary, error) {
	latestMap, stableMap, unstableMap := ComputeLatestStableUnstable(allVersionsMap)

	// collect all package names
	nameSet := make(map[string]struct{})
	for name := range allVersionsMap {
		nameSet[name] = struct{}{}
	}
	for name := range wikiVersionsMap {
		nameSet[name] = struct{}{}
	}

	// stable iteration order
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })

	var summaries []PackageVersionSummary
	for _, name := range names {
		display := name
		if vs := allVersionsMap[name]; len(vs) > 0 && strings.TrimSpace(vs[0].DisplayName) != "" {
			display = vs[0].DisplayName
		}
		s := PackageVersionSummary{Name: name, DisplayName: display}
		if v, ok := latestMap[name]; ok {
			vv := v
			s.LatestVersion = &vv
		}
		if v, ok := stableMap[name]; ok {
			vv := v
			s.LatestStable = &vv
		}
		if v, ok := unstableMap[name]; ok {
			vv := v
			s.LatestUnstable = &vv
		}

		// include wiki versions that we also know about
		if wikiV, ok := wikiVersionsMap[name]; ok {
			known := make(map[string]struct{})
			for _, pv := range allVersionsMap[name] {
				known[pv.Version] = struct{}{}
			}
			var filtered []string
			for _, wv := range wikiV {
				if _, ok := known[wv]; ok {
					filtered = append(filtered, wv)
				}
			}
			sort.Slice(filtered, func(i, j int) bool {
				vi, ei := semver.NewVersion(filtered[i])
				vj, ej := semver.NewVersion(filtered[j])
				if ei != nil || ej != nil {
					return strings.Compare(filtered[i], filtered[j]) < 0
				}
				return vi.LessThan(vj)
			})
			s.WikiVersions = filtered
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}

// GenerateVersionSummaryWikiTableWithWikiVersions renders a MediaWiki table with version information.
func GenerateVersionSummaryWikiTableWithWikiVersions(wikiVersionsMap map[string][]string, allVersionsMap map[string][]apiclient.Package) (string, error) {
	summaries, err := GetVersionSummaryTableWithWikiVersions(wikiVersionsMap, allVersionsMap)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("{| class=\"wikitable sortable\"\n")
	sb.WriteString("|-\n")
	sb.WriteString("! Name\n")
	sb.WriteString("! Display Name\n")
	sb.WriteString("! Latest Version(s)\n")

	for _, s := range summaries {
		sb.WriteString("|-\n")
		sb.WriteString(fmt.Sprintf("| %s\n", sanitizeForWiki(s.Name)))
		sb.WriteString(fmt.Sprintf("| %s\n", sanitizeForWiki(s.DisplayName)))
		sb.WriteString("| style=\"white-space: nowrap;\" | \n")
		if s.LatestVersion != nil {
			sb.WriteString("\n")
			sb.WriteString(fmt.Sprintf("* [[Template:VPM/%s/Latest version|Latest version]] ([[Template:VPM/%s/%s|%s]])\n",
				sanitizeForWiki(s.Name), sanitizeForWiki(s.Name), sanitizeForWiki(s.LatestVersion.Version), sanitizeForWiki(s.LatestVersion.Version)))
		}
		if s.LatestStable != nil {
			sb.WriteString("\n")
			sb.WriteString(fmt.Sprintf("* [[Template:VPM/%s/Latest stable version|Latest stable version]] ([[Template:VPM/%s/%s|%s]])\n",
				sanitizeForWiki(s.Name), sanitizeForWiki(s.Name), sanitizeForWiki(s.LatestStable.Version), sanitizeForWiki(s.LatestStable.Version)))
		}
		if s.LatestUnstable != nil {
			sb.WriteString("\n")
			sb.WriteString(fmt.Sprintf("* [[Template:VPM/%s/Latest unstable version|Latest unstable version]] ([[Template:VPM/%s/%s|%s]])\n",
				sanitizeForWiki(s.Name), sanitizeForWiki(s.Name), sanitizeForWiki(s.LatestUnstable.Version), sanitizeForWiki(s.LatestUnstable.Version)))
		}
		if len(s.WikiVersions) > 0 {
			for _, v := range s.WikiVersions {
				sb.WriteString("\n")
				sb.WriteString(fmt.Sprintf("* [[Template:VPM/%s/%s|%s]]\n", sanitizeForWiki(s.Name), sanitizeForWiki(v), sanitizeForWiki(v)))
			}
		}
	}
	sb.WriteString("|}\n")
	return sb.String(), nil
}

// BuildAllVersionsMapFromAPI converts API packages into an allVersionsMap keyed by package name.
func BuildAllVersionsMapFromAPI(pkgs []apiclient.Package) map[string][]apiclient.Package {
	result := make(map[string][]apiclient.Package)
	for _, p := range pkgs {
		result[p.Name] = append(result[p.Name], p)
	}
	return result
}
