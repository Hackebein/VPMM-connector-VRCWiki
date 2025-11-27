package mediawiki

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	apiclient "github.com/hackebein/vpmm/apps/vrcwiki-connector/pkg/apiclient"
)

type WikiConfig struct {
	URL       string
	Username  string
	Password  string
	Header    string
	HeaderVal string
}

type MediaWikiClient struct {
	apiURL     string
	httpClient *http.Client
	userAgent  string
	tokens     map[string]string
	mu         sync.RWMutex

	username string
	password string

	// optional extra header
	headerName  string
	headerValue string

	// offline mode configuration
	offline   bool
	outputDir string

	logger *slog.Logger
}

// buildVersion holds the version injected at build time via -ldflags. Defaults to "dev".
var buildVersion = "dev"

func getUserAgent() string {
	v := strings.TrimSpace(buildVersion)
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("VPMM-WikiSync/%s hackebein@gmail.com", v)
}

func NewMediaWikiClient(config WikiConfig, httpClient *http.Client) (*MediaWikiClient, error) {
	if httpClient == nil {
		jar, _ := cookiejar.New(nil)
		httpClient = &http.Client{Jar: jar}
	} else if httpClient.Jar == nil {
		jar, _ := cookiejar.New(nil)
		httpClient.Jar = jar
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	c := &MediaWikiClient{
		apiURL:      config.URL,
		httpClient:  httpClient,
		userAgent:   getUserAgent(),
		tokens:      make(map[string]string),
		username:    strings.TrimSpace(config.Username),
		password:    strings.TrimSpace(config.Password),
		headerName:  strings.TrimSpace(config.Header),
		headerValue: strings.TrimSpace(config.HeaderVal),
		logger:      logger,
	}

	// enable offline mode when no username/password provided
	if c.username == "" && c.password == "" {
		c.offline = true
		c.outputDir = "./wiki-output"
		if c.logger != nil {
			c.logger.Info("offline mode enabled: writing wiki pages to files", "dir", c.outputDir)
		}
	}

	if c.username != "" && c.password != "" {
		if err := c.Login(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func sanitizeForWiki(text string) string {
	text = strings.ReplaceAll(text, "|", "{{!}}")
	text = strings.ReplaceAll(text, "=", "{{=}}")
	return text
}

// sanitizeFilename converts a page title to a safe, flattened filename with .md extension.
// It replaces characters not allowed in filenames: <>:\"/\\|?* and ASCII control chars with '_',
// collapses multiple underscores, and trims leading/trailing spaces/underscores.
func sanitizeFilename(title string) string {
	title = strings.TrimSpace(title)
	var b strings.Builder
	for _, r := range title {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	// collapse multiple underscores
	var out strings.Builder
	prevUnderscore := false
	for _, r := range s {
		if r == '_' {
			if !prevUnderscore {
				out.WriteRune(r)
				prevUnderscore = true
			}
			continue
		}
		out.WriteRune(r)
		prevUnderscore = false
	}
	s = strings.Trim(out.String(), " _")
	if s == "" {
		s = "page"
	}
	return s + ".md"
}

func (c *MediaWikiClient) pageFilePath(title string) string {
	dir := c.outputDir
	if strings.TrimSpace(dir) == "" {
		dir = "./wiki-output"
	}
	return filepath.Join(dir, sanitizeFilename(title))
}

// UpdateSinglePackage performs a create-or-update flow for a package's Latest_version subtree.
// Unlike the gated helpers, this will create missing pages as needed.
func (c *MediaWikiClient) UpdateSinglePackage(pkg apiclient.Package) error {
	packageName := pkg.Name
	updated := 0
	// helpers for optional fields
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	pagesToUpdate := map[string]string{
		fmt.Sprintf("Template:VPM/%s/Latest_version", packageName):             sanitizeForWiki(pkg.Version),
		fmt.Sprintf("Template:VPM/%s/Latest_version/Description", packageName): sanitizeForWiki(str(pkg.Description)),
		fmt.Sprintf("Template:VPM/%s/Latest_version/DisplayName", packageName): sanitizeForWiki(pkg.DisplayName),
		fmt.Sprintf("Template:VPM/%s/Latest_version/License", packageName):     sanitizeForWiki(str(pkg.License)),
		fmt.Sprintf("Template:VPM/%s/Latest_version/VPM", packageName):         sanitizeForWiki(firstListingURL(pkg.Urls)),
	}
	if pkg.Author.Name != nil && *pkg.Author.Name != "" {
		authors := strings.Split(*pkg.Author.Name, ",")
		if len(authors) > 4 {
			authors = authors[:4]
		}
		for i, author := range authors {
			author = strings.TrimSpace(author)
			if author != "" {
				pagesToUpdate[fmt.Sprintf("Template:VPM/%s/Latest_version/Author_%d", packageName, i+1)] = sanitizeForWiki(author)
			}
		}
	}
	for title, newContent := range pagesToUpdate {
		currentContent, err := c.getPageContent(title)
		if err != nil {
			if !strings.Contains(err.Error(), "page does not exist") {
				// ignore read errors but proceed to write
				_ = err
			}
			currentContent = ""
		}
		if strings.TrimSpace(currentContent) != strings.TrimSpace(newContent) {
			if err := c.EditPage(title, newContent, true); err == nil {
				updated++
			}
		}
	}
	if c.logger != nil {
		c.logger.Info("wiki package updated", "package", packageName, "updated", updated)
	}
	return nil
}

func firstListingURL(urls *[]string) string {
	if urls == nil {
		return ""
	}
	for _, entry := range *urls {
		if strings.TrimSpace(entry) != "" {
			return entry
		}
	}
	return ""
}

func (c *MediaWikiClient) apiRequest(params map[string]string) (map[string]any, error) {
	params["format"] = "json"

	// legacy compatibility: also allow env-driven header injection
	if c.headerName == "" && c.headerValue == "" {
		if hn, hv := os.Getenv("VRCWIKI_AUTHORIZATION_HEADER"), os.Getenv("VRCWIKI_AUTHORIZATION_VALUE"); hn != "" && hv != "" {
			c.headerName, c.headerValue = hn, hv
		}
	}

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, c.apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.userAgent)
	if c.headerName != "" && c.headerValue != "" {
		req.Header.Set(c.headerName, c.headerValue)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	if e, ok := result["error"].(map[string]any); ok {
		code, _ := e["code"].(string)
		info, _ := e["info"].(string)
		return nil, fmt.Errorf("API error: %s - %s", code, info)
	}
	return result, nil
}

func (c *MediaWikiClient) getToken(tokenType string) (string, error) {
	c.mu.RLock()
	if t, ok := c.tokens[tokenType]; ok {
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[tokenType]; ok {
		return t, nil
	}
	params := map[string]string{"action": "query", "meta": "tokens", "type": tokenType}
	result, err := c.apiRequest(params)
	if err != nil {
		return "", fmt.Errorf("get %s token: %w", tokenType, err)
	}
	query, ok := result["query"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response: missing query")
	}
	tokens, ok := query["tokens"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response: missing tokens")
	}
	tokenKey := tokenType + "token"
	token, ok := tokens[tokenKey].(string)
	if !ok {
		return "", fmt.Errorf("token not found in response")
	}
	c.tokens[tokenType] = token
	return token, nil
}

func (c *MediaWikiClient) invalidateToken(tokenType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, tokenType)
}

func isBadTokenError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "badtoken")
}

func (c *MediaWikiClient) reloginIfPossible() error {
	if c.offline || c.username == "" || c.password == "" {
		return nil
	}
	c.invalidateToken("login")
	if err := c.Login(); err != nil {
		return fmt.Errorf("re-login after badtoken: %w", err)
	}
	return nil
}

func (c *MediaWikiClient) withCSRFWriteRetry(op func(csrf string) error) error {
	const maxAttempts = 2
	var lastErr error
	for range maxAttempts {
		csrf, err := c.getToken("csrf")
		if err != nil {
			return fmt.Errorf("get csrf: %w", err)
		}
		lastErr = op(csrf)
		if lastErr == nil {
			return nil
		}
		if !isBadTokenError(lastErr) {
			return lastErr
		}
		c.invalidateToken("csrf")
		if err := c.reloginIfPossible(); err != nil {
			return err
		}
	}
	return lastErr
}

func (c *MediaWikiClient) Login() error {
	loginToken, err := c.getToken("login")
	if err != nil {
		return fmt.Errorf("get login token: %w", err)
	}
	params := map[string]string{
		"action":     "login",
		"lgname":     c.username,
		"lgpassword": c.password,
		"lgtoken":    loginToken,
	}
	result, err := c.apiRequest(params)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	login, ok := result["login"].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid login response structure")
	}
	if r, _ := login["result"].(string); r != "Success" {
		reason, _ := login["reason"].(string)
		if reason == "" {
			reason = "unknown"
		}
		return fmt.Errorf("login failed: %s", reason)
	}
	c.mu.Lock()
	c.tokens = make(map[string]string)
	c.mu.Unlock()
	if c.logger != nil {
		c.logger.Info("wiki login success")
	}
	return nil
}

func (c *MediaWikiClient) EditPage(title, text string, bot bool) error {
	trimmedNew := strings.TrimSpace(text)
	currentContent, err := c.getPageContent(title)
	var summary string
	if err != nil {
		if !strings.Contains(err.Error(), "page does not exist") {
			return fmt.Errorf("get current content for page %s: %w", title, err)
		}
		currentContent = ""
		summary = fmt.Sprintf("Set: `%s`", text)
	} else {
		trimmedCurrent := strings.TrimSpace(currentContent)
		if trimmedCurrent == trimmedNew {
			return nil
		}
		if trimmedCurrent == "" {
			summary = fmt.Sprintf("Set: `%s`", text)
		} else {
			summary = fmt.Sprintf("`%s` => `%s`", trimmedCurrent, text)
		}
	}

	if c.offline {
		if err := os.MkdirAll(c.outputDir, 0o755); err != nil {
			return fmt.Errorf("ensure output dir: %w", err)
		}
		path := c.pageFilePath(title)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
		if c.logger != nil {
			c.logger.Info("offline write success", "title", title, "file", path, "bot", bot)
		}
		return nil
	}

	return c.withCSRFWriteRetry(func(csrf string) error {
		params := map[string]string{
			"action":  "edit",
			"title":   title,
			"text":    text,
			"summary": summary,
			"token":   csrf,
		}
		if bot {
			params["bot"] = "true"
		}
		result, err := c.apiRequest(params)
		if err != nil {
			return fmt.Errorf("edit request failed: %w", err)
		}
		edit, ok := result["edit"].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid edit response structure")
		}
		if r, _ := edit["result"].(string); r != "Success" {
			return fmt.Errorf("edit failed: %s", r)
		}
		if c.logger != nil {
			c.logger.Info("wiki edit success", "title", title, "bot", bot)
		}
		return nil
	})
}

func (c *MediaWikiClient) getPageContent(title string) (string, error) {
	if c.offline {
		path := c.pageFilePath(title)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("page does not exist: %s", title)
			}
			return "", fmt.Errorf("read file: %w", err)
		}
		return string(data), nil
	}
	params := map[string]string{
		"action":  "query",
		"titles":  title,
		"prop":    "revisions",
		"rvprop":  "content",
		"rvslots": "main",
	}
	result, err := c.apiRequest(params)
	if err != nil {
		return "", fmt.Errorf("get page content for %s: %w", title, err)
	}
	query, ok := result["query"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response structure: missing query")
	}
	pages, ok := query["pages"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid response structure: missing pages")
	}
	for _, page := range pages {
		pageMap, _ := page.(map[string]any)
		if pageMap == nil {
			continue
		}
		if _, missing := pageMap["missing"]; missing {
			return "", fmt.Errorf("page does not exist: %s", title)
		}
		revisions, _ := pageMap["revisions"].([]any)
		if len(revisions) == 0 {
			return "", fmt.Errorf("no revisions found for page: %s", title)
		}
		rev, _ := revisions[0].(map[string]any)
		slots, _ := rev["slots"].(map[string]any)
		main, _ := slots["main"].(map[string]any)
		content, _ := main["*"].(string)
		return content, nil
	}
	return "", fmt.Errorf("could not extract content from page: %s", title)
}

// DeletePage deletes a wiki page by title with an optional reason.
func (c *MediaWikiClient) DeletePage(title string, reason string) error {
	if c.offline {
		path := c.pageFilePath(title)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete file: %w", err)
		}
		if c.logger != nil {
			c.logger.Info("offline delete success", "title", title, "file", path, "reason", strings.TrimSpace(reason))
		}
		return nil
	}
	return c.withCSRFWriteRetry(func(csrf string) error {
		params := map[string]string{
			"action": "delete",
			"title":  title,
			"token":  csrf,
		}
		if reason != "" {
			params["reason"] = reason
		}
		result, err := c.apiRequest(params)
		if err != nil {
			return fmt.Errorf("delete request failed: %w", err)
		}
		if _, ok := result["delete"].(map[string]any); !ok {
			return fmt.Errorf("invalid delete response structure")
		}
		if c.logger != nil {
			c.logger.Info("wiki delete success", "title", title)
		}
		return nil
	})
}

// pageExists returns true if the given page exists on the wiki.
// It uses getPageContent and interprets "page does not exist" as non-existence.
func (c *MediaWikiClient) pageExists(title string) (bool, error) {
	_, err := c.getPageContent(title)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "page does not exist") {
		return false, nil
	}
	return false, err
}

// getAllPages retrieves all pages with the specified prefix, handling Template namespace and pagination.
func (c *MediaWikiClient) getAllPages(prefix string) ([]string, error) {
	var allPages []string
	apcontinue := ""

	var namespace string
	var actualPrefix string
	if strings.HasPrefix(prefix, "Template:") {
		namespace = "10"
		actualPrefix = strings.TrimPrefix(prefix, "Template:")
	} else {
		namespace = "0"
		actualPrefix = prefix
	}

	for {
		params := map[string]string{
			"action":      "query",
			"list":        "allpages",
			"apnamespace": namespace,
			"apprefix":    actualPrefix,
			"aplimit":     "500",
		}
		if apcontinue != "" {
			params["apcontinue"] = apcontinue
		}
		result, err := c.apiRequest(params)
		if err != nil {
			return nil, fmt.Errorf("get pages with prefix %s: %w", prefix, err)
		}
		query, ok := result["query"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid response structure: missing query")
		}
		pages, ok := query["allpages"].([]any)
		if !ok {
			return nil, fmt.Errorf("invalid response structure: missing allpages")
		}
		for _, p := range pages {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			title, _ := pm["title"].(string)
			if title != "" {
				allPages = append(allPages, title)
			}
		}
		if cont, ok := result["continue"].(map[string]any); ok {
			if apc, _ := cont["apcontinue"].(string); apc != "" {
				apcontinue = apc
				continue
			}
		}
		break
	}
	return allPages, nil
}

// parseVPMPageTitle parses a VPM page title and extracts package name, page type, and version/tag.
// Returns: packageName, pageType, versionTag
func parseVPMPageTitle(title string) (string, string, string) {
	if !strings.HasPrefix(title, "Template:VPM/") {
		return "", "", ""
	}
	remainder := strings.TrimPrefix(title, "Template:VPM/")
	parts := strings.Split(remainder, "/")
	if len(parts) < 2 {
		return "", "", ""
	}
	packageName := parts[0]

	normalize := func(s string) string {
		return strings.ReplaceAll(s, "_", " ")
	}
	second := normalize(parts[1])

	switch second {
	case "Latest version":
		if len(parts) == 2 {
			return packageName, "latest_version", ""
		}
		return packageName, "latest_version_subpage", parts[2]
	case "Latest stable version":
		if len(parts) == 2 {
			return packageName, "latest_stable_version", ""
		}
		return packageName, "latest_stable_version_subpage", parts[2]
	case "Latest unstable version":
		if len(parts) == 2 {
			return packageName, "latest_unstable_version", ""
		}
		return packageName, "latest_unstable_version_subpage", parts[2]
	default:
		versionTag := parts[1]
		if len(parts) == 2 {
			// must be a specific version page; ensure semver-only checking is handled by caller
			// but we still return it here
			return packageName, "version", versionTag
		}
		return packageName, "version_subpage", versionTag
	}
}

// ProcessSpecificVersionPage handles a specific version page (semver-only).
// Gated: only updates when the specific version page already exists.
func (c *MediaWikiClient) ProcessSpecificVersionPage(packageName, versionTag string, knownVersions map[string]apiclient.Package) error {
	versionPageTitle := fmt.Sprintf("Template:VPM/%s/%s", packageName, versionTag)
	// gate: only proceed if the specific version page already exists
	exists, err := c.pageExists(versionPageTitle)
	if err != nil {
		return fmt.Errorf("check existence for %s: %w", versionPageTitle, err)
	}
	if !exists {
		return nil
	}
	// read version from the page content, allowing free-form page names
	content, err := c.getPageContent(versionPageTitle)
	if err != nil {
		return fmt.Errorf("read content from %s: %w", versionPageTitle, err)
	}
	v, err := semver.StrictNewVersion(strings.TrimSpace(content))
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("non-semver version content on page", "package", packageName, "page", versionPageTitle, "content", strings.TrimSpace(content))
		}
		return nil
	}
	// if known, update subpages for this version (main page content is the source of truth)
	if pkgVersion, ok := knownVersions[v.String()]; ok {
		return c.updateVersionSubpages(packageName, versionTag, pkgVersion)
	}
	if c.logger != nil {
		c.logger.Info("version from page content not found in known versions", "package", packageName, "version", v.String(), "page", versionPageTitle)
	}
	return nil
}

// updateVersionSubpages updates the subpages for a version (either Latest_* or specific version tag)
func (c *MediaWikiClient) updateVersionSubpages(packageName, versionPath string, version apiclient.Package) error {
	// helpers
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}

	// Description
	descTitle := fmt.Sprintf("Template:VPM/%s/%s/Description", packageName, versionPath)
	if err := c.EditPage(descTitle, sanitizeForWiki(str(version.Description)), true); err != nil {
		return fmt.Errorf("update description page: %w", err)
	}
	// DisplayName
	dnTitle := fmt.Sprintf("Template:VPM/%s/%s/DisplayName", packageName, versionPath)
	if err := c.EditPage(dnTitle, sanitizeForWiki(version.DisplayName), true); err != nil {
		return fmt.Errorf("update display name page: %w", err)
	}
	// License
	licTitle := fmt.Sprintf("Template:VPM/%s/%s/License", packageName, versionPath)
	if err := c.EditPage(licTitle, sanitizeForWiki(str(version.License)), true); err != nil {
		return fmt.Errorf("update license page: %w", err)
	}
	// VPM (first listing URL)
	listingURL := firstListingURL(version.Urls)
	vpmTitle := fmt.Sprintf("Template:VPM/%s/%s/VPM", packageName, versionPath)
	if err := c.EditPage(vpmTitle, sanitizeForWiki(listingURL), true); err != nil {
		return fmt.Errorf("update VPM page: %w", err)
	}

	// Authors handling
	authorName := str(version.Author.Name)
	if strings.TrimSpace(authorName) != "" {
		authors := strings.Split(authorName, ",")
		for i := range authors {
			authors[i] = strings.TrimSpace(authors[i])
		}
		if len(authors) > 4 {
			authors = authors[:4]
		}
		for i, author := range authors {
			if author == "" {
				continue
			}
			aTitle := fmt.Sprintf("Template:VPM/%s/%s/Author_%d", packageName, versionPath, i+1)
			if err := c.EditPage(aTitle, sanitizeForWiki(author), true); err != nil {
				return fmt.Errorf("update Author_%d page: %w", i+1, err)
			}
		}
		// cleanup any leftover author pages up to 4
		for i := len(authors) + 1; i <= 4; i++ {
			aTitle := fmt.Sprintf("Template:VPM/%s/%s/Author_%d", packageName, versionPath, i)
			if _, err := c.getPageContent(aTitle); err == nil {
				_ = c.DeletePage(aTitle, "Author removed from package")
			}
		}
	} else {
		// no authors; cleanup possible existing pages 1..4
		for i := 1; i <= 4; i++ {
			aTitle := fmt.Sprintf("Template:VPM/%s/%s/Author_%d", packageName, versionPath, i)
			if _, err := c.getPageContent(aTitle); err == nil {
				_ = c.DeletePage(aTitle, "Author removed from package")
			}
		}
	}
	return nil
}

// UpdateLatestVersionPages updates the Latest_version page and its subpages for a package.
// Gated: only updates when the Latest_version page already exists.
func (c *MediaWikiClient) UpdateLatestVersionPages(version apiclient.Package) error {
	pkg := version.Name
	title := fmt.Sprintf("Template:VPM/%s/Latest_version", pkg)
	// gate: only update if main page already exists
	exists, err := c.pageExists(title)
	if err != nil {
		return fmt.Errorf("check existence for %s: %w", title, err)
	}
	if !exists {
		return nil
	}
	if err := c.EditPage(title, sanitizeForWiki(version.Version), true); err != nil {
		return fmt.Errorf("update latest version page: %w", err)
	}
	return c.updateVersionSubpages(pkg, "Latest_version", version)
}

// UpdateLatestStableVersionPages updates the Latest_stable_version page and its subpages.
// Gated: only updates when the Latest_stable_version page already exists.
func (c *MediaWikiClient) UpdateLatestStableVersionPages(version apiclient.Package) error {
	pkg := version.Name
	title := fmt.Sprintf("Template:VPM/%s/Latest_stable_version", pkg)
	// gate: only update if main page already exists
	exists, err := c.pageExists(title)
	if err != nil {
		return fmt.Errorf("check existence for %s: %w", title, err)
	}
	if !exists {
		return nil
	}
	if err := c.EditPage(title, sanitizeForWiki(version.Version), true); err != nil {
		return fmt.Errorf("update latest stable version page: %w", err)
	}
	return c.updateVersionSubpages(pkg, "Latest_stable_version", version)
}

// UpdateLatestUnstableVersionPages updates the Latest_unstable_version page and its subpages.
// Gated: only updates when the Latest_unstable_version page already exists.
func (c *MediaWikiClient) UpdateLatestUnstableVersionPages(version apiclient.Package) error {
	pkg := version.Name
	title := fmt.Sprintf("Template:VPM/%s/Latest_unstable_version", pkg)
	// gate: only update if main page already exists
	exists, err := c.pageExists(title)
	if err != nil {
		return fmt.Errorf("check existence for %s: %w", title, err)
	}
	if !exists {
		return nil
	}
	if err := c.EditPage(title, sanitizeForWiki(version.Version), true); err != nil {
		return fmt.Errorf("update latest unstable version page: %w", err)
	}
	return c.updateVersionSubpages(pkg, "Latest_unstable_version", version)
}

// ScanVpmPages scans the wiki for all Template:VPM/* pages and returns
// a map of package -> pages and a map of package -> known version tags on the wiki.
func (c *MediaWikiClient) ScanVpmPages() (map[string][]string, map[string][]string, error) {
	pages, err := c.getAllPages("Template:VPM/")
	if err != nil {
		return nil, nil, err
	}
	packagePages := make(map[string][]string)
	wikiVersions := make(map[string][]string)
	for _, p := range pages {
		pkg, pageType, versionTag := parseVPMPageTitle(p)
		if pkg == "" {
			continue
		}
		packagePages[pkg] = append(packagePages[pkg], p)
		if pageType == "version" && strings.TrimSpace(versionTag) != "" {
			// add if not already present
			exists := slices.Contains(wikiVersions[pkg], versionTag)
			if !exists {
				wikiVersions[pkg] = append(wikiVersions[pkg], versionTag)
			}
		}
	}
	return packagePages, wikiVersions, nil
}

// SyncExistingPages updates only those pages whose main pages already exist on the wiki.
// It mirrors the legacy behavior: Latest_*, Latest_* subpages, and specific version subpages
// are updated only when their corresponding main page exists.
func (c *MediaWikiClient) SyncExistingPages(
	latest map[string]apiclient.Package,
	stable map[string]apiclient.Package,
	unstable map[string]apiclient.Package,
	allByPkg map[string]map[string]apiclient.Package,
) error {
	packagePages, wikiVersionsMap, err := c.ScanVpmPages()
	if err != nil {
		return err
	}
	// union of package names
	nameSet := make(map[string]struct{})
	for n := range packagePages {
		nameSet[n] = struct{}{}
	}
	for n := range latest {
		nameSet[n] = struct{}{}
	}
	for n := range stable {
		nameSet[n] = struct{}{}
	}
	for n := range unstable {
		nameSet[n] = struct{}{}
	}
	var errs []string
	for name := range nameSet {
		pages := packagePages[name]
		has := func(title string) bool {
			return slices.Contains(pages, title)
		}
		// Latest version
		if v, ok := latest[name]; ok {
			title := fmt.Sprintf("Template:VPM/%s/Latest_version", name)
			if has(title) {
				if err := c.UpdateLatestVersionPages(v); err != nil {
					errs = append(errs, fmt.Sprintf("latest %s: %v", name, err))
				}
			}
		}
		// Latest stable
		if v, ok := stable[name]; ok {
			title := fmt.Sprintf("Template:VPM/%s/Latest_stable_version", name)
			if has(title) {
				if err := c.UpdateLatestStableVersionPages(v); err != nil {
					errs = append(errs, fmt.Sprintf("stable %s: %v", name, err))
				}
			}
		}
		// Latest unstable
		if v, ok := unstable[name]; ok {
			title := fmt.Sprintf("Template:VPM/%s/Latest_unstable_version", name)
			if has(title) {
				if err := c.UpdateLatestUnstableVersionPages(v); err != nil {
					errs = append(errs, fmt.Sprintf("unstable %s: %v", name, err))
				}
			}
		}
		// Specific version pages discovered on the wiki
		known := allByPkg[name]
		if versions, ok := wikiVersionsMap[name]; ok && len(versions) > 0 && known != nil {
			for _, tag := range versions {
				if err := c.ProcessSpecificVersionPage(name, tag, known); err != nil {
					errs = append(errs, fmt.Sprintf("version %s/%s: %v", name, tag, err))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sync existing pages: %d errors:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}
