package recipes

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxBodySize = 5 * 1024 * 1024 // 5 MB

// acceptHTML and acceptImage are what a browser asks for on a navigation and
// on an <img>, which is what the importer is standing in for.
const (
	acceptHTML  = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	acceptImage = "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"
)

// A bot wall answers a share of requests with a challenge rather than all of
// them: Cloudflare scores the Go client on its TLS and HTTP/2 fingerprint, not
// its User-Agent, and challenged roughly half of cold connections to
// allrecipes.com in testing. Retrying on the same client rides the connection
// that was already admitted, which clears most of them. No header we can set
// clears the rest.
const (
	maxFetchAttempts = 3
	fetchRetryDelay  = 300 * time.Millisecond
)

// defaultUserAgent identifies the importer when the request that asked for the
// import carried no usable one of its own.
const defaultUserAgent = "Cog/1.0 (Recipe Importer)"

// maxUserAgentLen bounds a forwarded User-Agent. Real ones are well under this;
// anything longer is not a browser and does not need to be passed on.
const maxUserAgentLen = 256

// ImportFromURLResult holds the result of importing a recipe from a URL.
type ImportFromURLResult struct {
	RecipeID int64
}

// FetchAndImport fetches a URL, extracts recipe JSON-LD, and stores it.
//
// userAgent is the one from the request that asked for the import — recipe
// sites routinely serve a 403 to anything that does not look like a browser,
// and the person importing is sitting in front of a browser that the site
// would have served. It is sanitized, not trusted: it reaches a third party.
func (p *Plugin) FetchAndImport(db *sql.DB, username, rawURL, userAgent string) (*ImportFromURLResult, error) {
	if err := validateURL(rawURL); err != nil {
		return nil, err
	}

	ua := sanitizeUserAgent(userAgent)
	resp, err := p.fetch(rawURL, ua, acceptHTML)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	recipe, err := ExtractRecipeFromHTML(string(body))
	if err != nil {
		return nil, fmt.Errorf("extract recipe: %w", err)
	}

	recipe.SourceURL = rawURL

	id, err := SaveImportedRecipe(db, recipe)
	if err != nil {
		return nil, fmt.Errorf("save recipe: %w", err)
	}

	// Try to download the image
	if recipe.ImageURL != "" {
		if err := p.downloadImage(username, id, recipe.ImageURL, ua); err != nil {
			p.log.Warn("failed to download recipe image", "url", recipe.ImageURL, "err", err)
		} else {
			ext := imageExtFromURL(recipe.ImageURL)
			imgPath := fmt.Sprintf("%d%s", id, ext)
			db.Exec(`UPDATE recipes SET image_path = ? WHERE id = ?`, imgPath, id)
		}
	}

	return &ImportFromURLResult{RecipeID: id}, nil
}

// fetch GETs a URL as the importing browser would, retrying a bot-wall
// challenge. Returns a response with an unread body on success only; every
// other outcome has already closed what it opened.
func (p *Plugin) fetch(rawURL, ua, accept string) (*http.Response, error) {
	var lastStatus int
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", accept)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch URL: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		lastStatus = resp.StatusCode
		retryable := isBotWall(resp)
		// Draining is what lets the retry reuse this connection, and reusing
		// it is the whole point.
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodySize))
		resp.Body.Close()

		if !retryable {
			return nil, fmt.Errorf("unexpected status: %d", lastStatus)
		}
		if attempt == maxFetchAttempts {
			return nil, fmt.Errorf("blocked by the site's bot protection after %d attempts (status %d)", maxFetchAttempts, lastStatus)
		}
		time.Sleep(fetchRetryDelay)
	}
}

// isBotWall reports whether a response looks like an anti-bot challenge rather
// than a considered refusal. Cloudflare labels its own; for anything else a
// 403 or 503 on a page a browser can open is the only signal available.
func isBotWall(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusServiceUnavailable:
		return true
	default:
		return resp.Header.Get("cf-mitigated") == "challenge"
	}
}

// downloadImage fetches an image and saves it to the recipes images directory.
func (p *Plugin) downloadImage(username string, recipeID int64, imageURL, userAgent string) error {
	if err := validateURL(imageURL); err != nil {
		return err
	}

	resp, err := p.fetch(imageURL, sanitizeUserAgent(userAgent), acceptImage)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return fmt.Errorf("not an image: %s", ct)
	}

	dir := p.imagesDir(username)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	ext := imageExtFromURL(imageURL)
	if ext == "" {
		ext = imageExtFromContentType(ct)
	}
	path := filepath.Join(dir, strconv.FormatInt(recipeID, 10)+ext)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, io.LimitReader(resp.Body, maxBodySize))
	return err
}

func imageExtFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ".jpg"
	}
	ext := filepath.Ext(u.Path)
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return ext
	default:
		return ".jpg"
	}
}

func imageExtFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".jpg"
	}
}

// sanitizeUserAgent returns a User-Agent safe to put on an outbound request.
// A header value is only allowed printable ASCII, and a caller controls this
// one, so anything else — or anything absurdly long — falls back to naming the
// importer rather than being repaired into something unrecognisable.
func sanitizeUserAgent(ua string) string {
	if ua == "" || len(ua) > maxUserAgentLen {
		return defaultUserAgent
	}
	for i := 0; i < len(ua); i++ {
		if ua[i] < 0x20 || ua[i] > 0x7e {
			return defaultUserAgent
		}
	}
	return ua
}

func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %s", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("missing host in URL")
	}
	return nil
}

// ParsedRecipe holds the normalized data extracted from JSON-LD.
type ParsedRecipe struct {
	Name         string
	Description  string
	SourceURL    string
	ImageURL     string
	Category     string
	Cuisine      string
	RecipeYield  string
	PrepTime     string
	CookTime     string
	TotalTime    string
	Calories     string
	Ingredients  []string
	Instructions []string
	Keywords     []string
	JSONLD       string // raw JSON-LD string
}

// ExtractRecipeFromHTML parses HTML and finds schema.org Recipe JSON-LD.
func ExtractRecipeFromHTML(html string) (*ParsedRecipe, error) {
	// Find all <script type="application/ld+json"> blocks
	blocks := extractJSONLDBlocks(html)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no JSON-LD blocks found")
	}

	for _, block := range blocks {
		recipe, err := parseRecipeJSONLD(block)
		if err == nil {
			return recipe, nil
		}
	}

	return nil, fmt.Errorf("no Recipe JSON-LD found in page")
}

// ImportRecipeFromJSON imports a recipe from raw JSON-LD.
func ImportRecipeFromJSON(jsonStr string) (*ParsedRecipe, error) {
	return parseRecipeJSONLD(jsonStr)
}

// extractJSONLDBlocks extracts content from <script type="application/ld+json"> tags.
func extractJSONLDBlocks(html string) []string {
	var blocks []string
	searchHTML := strings.ToLower(html)
	tag := `<script type="application/ld+json">`

	pos := 0
	for {
		start := strings.Index(searchHTML[pos:], tag)
		if start == -1 {
			break
		}
		start += pos + len(tag)
		end := strings.Index(searchHTML[start:], "</script>")
		if end == -1 {
			break
		}
		// Use original case HTML for the content
		blocks = append(blocks, strings.TrimSpace(html[start:start+end]))
		pos = start + end
	}
	return blocks
}

// parseRecipeJSONLD tries to parse a JSON-LD block as a Recipe.
func parseRecipeJSONLD(jsonStr string) (*ParsedRecipe, error) {
	// Try single object first
	var obj map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &obj); err == nil {
		if recipe, err := extractFromObject(obj, jsonStr); err == nil {
			return recipe, nil
		}
	}

	// Try array of objects
	var arr []map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &arr); err == nil {
		for _, obj := range arr {
			if recipe, err := extractFromObject(obj, jsonStr); err == nil {
				return recipe, nil
			}
		}
	}

	return nil, fmt.Errorf("no Recipe found in JSON-LD")
}

// extractFromObject tries to extract a Recipe from a JSON-LD object.
func extractFromObject(obj map[string]any, rawJSON string) (*ParsedRecipe, error) {
	// Direct Recipe type
	if isRecipeType(obj) {
		return normalizeRecipe(obj, rawJSON)
	}

	// Check @graph array
	if graph, ok := obj["@graph"]; ok {
		if items, ok := graph.([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok && isRecipeType(m) {
					// Re-marshal just this object for the stored JSON-LD
					itemJSON, _ := json.Marshal(m)
					return normalizeRecipe(m, string(itemJSON))
				}
			}
		}
	}

	return nil, fmt.Errorf("not a Recipe object")
}

func isRecipeType(obj map[string]any) bool {
	t := getString(obj, "@type")
	return strings.EqualFold(t, "recipe")
}

func normalizeRecipe(obj map[string]any, rawJSON string) (*ParsedRecipe, error) {
	r := &ParsedRecipe{
		Name:        getString(obj, "name"),
		Description: getString(obj, "description"),
		Category:    getString(obj, "recipeCategory"),
		Cuisine:     getString(obj, "recipeCuisine"),
		RecipeYield: getString(obj, "recipeYield"),
		PrepTime:    getString(obj, "prepTime"),
		CookTime:    getString(obj, "cookTime"),
		TotalTime:   getString(obj, "totalTime"),
		JSONLD:      rawJSON,
	}

	if r.Name == "" {
		return nil, fmt.Errorf("recipe has no name")
	}

	// Image
	r.ImageURL = extractImageURL(obj)

	// Calories from nutrition
	if nutrition, ok := obj["nutrition"].(map[string]any); ok {
		r.Calories = getString(nutrition, "calories")
	}

	// Ingredients
	r.Ingredients = getStringArray(obj, "recipeIngredient")

	// Instructions
	r.Instructions = extractInstructions(obj)

	// Keywords
	r.Keywords = extractKeywords(obj)

	return r, nil
}

func extractImageURL(obj map[string]any) string {
	img := obj["image"]
	if img == nil {
		return ""
	}
	switch v := img.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
			if m, ok := v[0].(map[string]any); ok {
				return getString(m, "url")
			}
		}
	case map[string]any:
		return getString(v, "url")
	}
	return ""
}

func extractInstructions(obj map[string]any) []string {
	raw := obj["recipeInstructions"]
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case string:
		// Single string of instructions
		lines := strings.Split(v, "\n")
		var result []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				result = append(result, line)
			}
		}
		return result
	case []any:
		var result []string
		for _, item := range v {
			switch step := item.(type) {
			case string:
				if s := strings.TrimSpace(step); s != "" {
					result = append(result, s)
				}
			case map[string]any:
				t := getString(step, "@type")
				if strings.EqualFold(t, "howtostep") {
					if text := getString(step, "text"); text != "" {
						result = append(result, text)
					}
				} else if strings.EqualFold(t, "howtosection") {
					// HowToSection contains itemListElement with HowToSteps
					if items, ok := step["itemListElement"].([]any); ok {
						for _, subItem := range items {
							if m, ok := subItem.(map[string]any); ok {
								if text := getString(m, "text"); text != "" {
									result = append(result, text)
								}
							}
						}
					}
				}
			}
		}
		return result
	}
	return nil
}

func extractKeywords(obj map[string]any) []string {
	raw := obj["keywords"]
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case string:
		parts := strings.Split(v, ",")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	case []any:
		return getStringArrayFromSlice(v)
	}
	return nil
}

// getString safely gets a string from a map.
func getString(obj map[string]any, key string) string {
	v, ok := obj[key]
	if !ok {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// getStringArray gets an array of strings from a JSON field.
func getStringArray(obj map[string]any, key string) []string {
	v, ok := obj[key]
	if !ok {
		return nil
	}
	switch a := v.(type) {
	case []any:
		return getStringArrayFromSlice(a)
	case string:
		return []string{a}
	}
	return nil
}

func getStringArrayFromSlice(a []any) []string {
	var result []string
	for _, item := range a {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// SaveImportedRecipe saves a ParsedRecipe into the database.
func SaveImportedRecipe(db *sql.DB, parsed *ParsedRecipe) (int64, error) {
	r := &Recipe{
		Name:        parsed.Name,
		Description: parsed.Description,
		SourceURL:   parsed.SourceURL,
		Category:    parsed.Category,
		Cuisine:     parsed.Cuisine,
		RecipeYield: parsed.RecipeYield,
		PrepTime:    parsed.PrepTime,
		CookTime:    parsed.CookTime,
		TotalTime:   parsed.TotalTime,
		Calories:    parsed.Calories,
		JSONLD:      parsed.JSONLD,
	}

	id, err := CreateRecipe(db, r)
	if err != nil {
		return 0, err
	}

	if err := SetIngredients(db, id, parsed.Ingredients); err != nil {
		return 0, fmt.Errorf("set ingredients: %w", err)
	}
	if err := SetInstructions(db, id, parsed.Instructions); err != nil {
		return 0, fmt.Errorf("set instructions: %w", err)
	}
	if err := SetKeywords(db, id, parsed.Keywords); err != nil {
		return 0, fmt.Errorf("set keywords: %w", err)
	}

	return id, nil
}
