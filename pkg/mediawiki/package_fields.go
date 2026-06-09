package mediawiki

import (
	"strings"

	apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
)

func PackageName(pkg apiclient.Package) string {
	return pkg.VPMPackage.VPMIdentifier.Name
}

func PackageVersion(pkg apiclient.Package) string {
	return pkg.VPMPackage.VPMIdentifier.Version
}

func packageDisplayName(pkg apiclient.Package) string {
	return pkg.VPMPackage.VPMIdentifier.DisplayName
}

func packageDescriptionPtr(pkg apiclient.Package) *string {
	return pkg.VPMPackage.Description
}

func packageLicensePtr(pkg apiclient.Package) *string {
	return pkg.VPMPackage.License
}

func packageAuthorPtr(pkg apiclient.Package) *apiclient.VPMAuthor {
	return pkg.VPMPackage.Author
}

// PackageFromIndexVersion maps a flat /index.json version entry to apiclient.Package.
func PackageFromIndexVersion(packageName string, v IndexPackageVersion) apiclient.Package {
	version := strings.TrimSpace(v.Version)
	if version == "" {
		version = strings.TrimSpace(v.VersionKey)
	}
	displayName := strings.TrimSpace(v.DisplayName)
	if displayName == "" {
		displayName = packageName
	}
	return apiclient.Package{
		VPMPackage: apiclient.VPMPackage{
			VPMIdentifier: apiclient.VPMIdentifier{
				Name:        packageName,
				Version:     version,
				DisplayName: displayName,
			},
			Description: v.Description,
			License:     v.License,
			Author:      v.Author,
		},
	}
}
