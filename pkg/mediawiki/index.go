package mediawiki

import apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"

// IndexPackageVersion is a flat VPM entry from /index.json (not the nested API Package shape).
type IndexPackageVersion struct {
	VersionKey  string `json:"-"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	DisplayName string `json:"displayName"`
	Description *string
	License     *string
	Author      *apiclient.VPMAuthor `json:"author,omitempty"`
}
