package mediawiki

import (
	"testing"

	apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
)

func TestPackageFromIndexVersion(t *testing.T) {
	desc := "desc"
	lic := "MIT"
	author := "Author"
	pkg := PackageFromIndexVersion("com.example.pkg", IndexPackageVersion{
		VersionKey:  "1.0.0",
		Name:        "ignored.name",
		Version:     "1.0.0",
		DisplayName: "Example",
		Description: &desc,
		License:     &lic,
		Author:      &apiclient.VPMAuthor{Name: &author},
	})

	if got := PackageName(pkg); got != "com.example.pkg" {
		t.Fatalf("PackageName = %q, want com.example.pkg", got)
	}
	if got := PackageVersion(pkg); got != "1.0.0" {
		t.Fatalf("PackageVersion = %q, want 1.0.0", got)
	}
	if got := packageDisplayName(pkg); got != "Example" {
		t.Fatalf("displayName = %q, want Example", got)
	}
}

func TestBuildAllVersionsMapFromAPIIndexEntries(t *testing.T) {
	pkgs := []apiclient.Package{
		PackageFromIndexVersion("com.a", IndexPackageVersion{VersionKey: "1.0.0", Version: "1.0.0"}),
		PackageFromIndexVersion("com.b", IndexPackageVersion{VersionKey: "2.0.0", Version: "2.0.0"}),
	}
	m := BuildAllVersionsMapFromAPI(pkgs)
	if len(m) != 2 {
		t.Fatalf("len(map) = %d, want 2", len(m))
	}
	if _, ok := m[""]; ok {
		t.Fatal("unexpected empty package name key")
	}
}
