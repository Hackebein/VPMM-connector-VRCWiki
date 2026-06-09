package mediawiki

import apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"

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
