package mediawiki

// RequestStats counts MediaWiki api.php calls made by MediaWikiClient.
type RequestStats struct {
	// Requests is the number of HTTP POSTs that received a response.
	Requests int
	Queries  int
	Edits    int
	Deletes  int
	Logins   int
	// Skips is unchanged EditPage calls that did not send action=edit.
	Skips int
}

// Sub returns the per-field difference a - b.
func (a RequestStats) Sub(b RequestStats) RequestStats {
	return RequestStats{
		Requests: a.Requests - b.Requests,
		Queries:  a.Queries - b.Queries,
		Edits:    a.Edits - b.Edits,
		Deletes:  a.Deletes - b.Deletes,
		Logins:   a.Logins - b.Logins,
		Skips:    a.Skips - b.Skips,
	}
}

// Stats returns a snapshot of MediaWiki API counters.
func (c *MediaWikiClient) Stats() RequestStats {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.stats
}

func (c *MediaWikiClient) recordRequest(action string) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	c.stats.Requests++
	switch action {
	case "query":
		c.stats.Queries++
	case "edit":
		c.stats.Edits++
	case "delete":
		c.stats.Deletes++
	case "login":
		c.stats.Logins++
	}
}

func (c *MediaWikiClient) recordSkip() {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	c.stats.Skips++
}
