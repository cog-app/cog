package recipes_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/recipes"
)

// sessionAuth returns HTTPOpts that set a session cookie and X-CSRF-Token header
// for a user, which is needed for endpoints registered via RegisterAction.
func sessionAuth(t testing.TB, env *cogtest.Env, u *cogtest.User) (cookie, csrf cogtest.HTTPOpt) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie = cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf = cogtest.Header("X-CSRF-Token", token)
	return cookie, csrf
}

// postAction sends a JSON POST to a RegisterAction endpoint with session+CSRF.
func postAction(t testing.TB, env *cogtest.Env, path string, body any, cookie, csrf cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = strings.NewReader(string(data))
	}
	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	return env.Do(t, req, cookie, csrf)
}

func putAction(t testing.TB, env *cogtest.Env, path string, body string, cookie, csrf cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	req := cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return env.Do(t, req, cookie, csrf)
}

func deleteAction(t testing.TB, env *cogtest.Env, path string, cookie, csrf cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	req := cogtest.NewRequest(t, "DELETE", env.Providers.Config.BaseURL+path, nil)
	return env.Do(t, req, cookie, csrf)
}

func postFormAction(t testing.TB, env *cogtest.Env, path string, form url.Values, cookie, csrf cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	return env.PostForm(t, path, form, cookie, csrf, cogtest.StopRedirect)
}

// ensureMigrated ensures the recipes DB is migrated for a user by calling Plugin.DB().
func ensureMigrated(t testing.TB, env *cogtest.Env, username string) {
	t.Helper()
	plug, _ := env.Providers.Plugins.Plugin("recipes")
	_, err := plug.(*recipes.Plugin).DB(username)
	if err != nil {
		t.Fatalf("migrate recipes db: %v", err)
	}
}

func TestRecipesIndexPage(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)

	env.Get(t, "/apps/recipes/", alice.Auth).
		Status(200).
		Contains("All recipes").
		Contains("No recipes yet")
}

func TestRecipesCRUD_API(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	recipeJSON := `{
		"@context": "https://schema.org",
		"@type": "Recipe",
		"name": "Test Pasta",
		"description": "A simple pasta",
		"recipeCategory": "Main Course",
		"recipeIngredient": ["200g pasta", "100g sauce"],
		"recipeInstructions": [
			{"@type": "HowToStep", "text": "Boil pasta"},
			{"@type": "HowToStep", "text": "Add sauce"}
		],
		"keywords": "italian, pasta"
	}`

	// Create
	var created struct {
		ID int64 `json:"id"`
	}
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(recipeJSON), cookie, csrf).
		Status(200).JSON(&created)
	require.True(t, created.ID > 0)
	id := fmt.Sprintf("%d", created.ID)

	// Get by ID (returns JSON-LD)
	var got map[string]any
	env.Get(t, "/apps/recipes/api/recipes/"+id, alice.Auth).
		Status(200).JSON(&got)
	require.Equal(t, "Test Pasta", got["name"])

	// Update
	updatedJSON := `{
		"@context": "https://schema.org",
		"@type": "Recipe",
		"name": "Updated Pasta",
		"description": "An updated pasta dish",
		"recipeCategory": "Main Course"
	}`
	putAction(t, env, "/apps/recipes/api/recipes/"+id, updatedJSON, cookie, csrf).
		Status(200)

	// Verify update
	var updated map[string]any
	env.Get(t, "/apps/recipes/api/recipes/"+id, alice.Auth).
		Status(200).JSON(&updated)
	require.Equal(t, "Updated Pasta", updated["name"])

	// Delete
	deleteAction(t, env, "/apps/recipes/api/recipes/"+id, cookie, csrf).
		Status(200)

	// Verify deleted
	env.Get(t, "/apps/recipes/api/recipes/"+id, alice.Auth).Status(404)
}

func TestRecipesListAPI(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	// Empty list
	var empty []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).
		Status(200).JSON(&empty)
	require.Equal(t, 0, len(empty))

	// Create two recipes
	for _, name := range []string{"Recipe One", "Recipe Two"} {
		body := fmt.Sprintf(`{"@type":"Recipe","name":"%s"}`, name)
		postAction(t, env, "/apps/recipes/api/recipes",
			json.RawMessage(body), cookie, csrf).Status(200)
	}

	var list []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).
		Status(200).JSON(&list)
	require.Equal(t, 2, len(list))
}

func TestRecipesCategoriesAPI(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	// Empty
	var empty []recipes.CategoryCount
	env.Get(t, "/apps/recipes/api/categories", alice.Auth).
		Status(200).JSON(&empty)
	require.Equal(t, 0, len(empty))

	// Create recipe with category
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"Soup","recipeCategory":"Soups"}`),
		cookie, csrf).Status(200)

	var cats []map[string]any
	env.Get(t, "/apps/recipes/api/categories", alice.Auth).
		Status(200).JSON(&cats)
	require.Equal(t, 1, len(cats))
	require.Equal(t, "Soups", cats[0]["Name"])
}

func TestRecipesKeywordsAPI(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"Pasta","keywords":"italian, easy"}`),
		cookie, csrf).Status(200)

	var kws []map[string]any
	env.Get(t, "/apps/recipes/api/keywords", alice.Auth).
		Status(200).JSON(&kws)
	require.Equal(t, 2, len(kws))
}

func TestRecipesImageAPI(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	// Create recipe (no image)
	var created struct {
		ID int64 `json:"id"`
	}
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"No Image"}`),
		cookie, csrf).Status(200).JSON(&created)
	id := fmt.Sprintf("%d", created.ID)

	// Image should be 404
	env.Get(t, "/apps/recipes/api/recipes/"+id+"/image", alice.Auth).Status(404)
}

func TestRecipesWebCRUD(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	// Create via form
	postFormAction(t, env, "/apps/recipes/recipes", url.Values{
		"name":         {"My Recipe"},
		"description":  {"A great recipe"},
		"category":     {"Desserts"},
		"ingredients":  {"1 cup flour\n2 eggs"},
		"instructions": {"Mix ingredients\nBake at 350"},
		"keywords":     {"baking, easy"},
		"prep_h":       {"1"},
		"prep_m":       {"30"},
		"cook_m":       {"45"},
		"calories":     {"410 kcal"},
	}, cookie, csrf).Status(303)

	// Verify recipe appears in list
	env.Get(t, "/apps/recipes/", alice.Auth).
		Status(200).
		Contains("My Recipe")

	// Get recipe ID from the list API
	var list []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).
		Status(200).JSON(&list)
	require.Equal(t, 1, len(list))
	id := fmt.Sprintf("%.0f", list[0]["recipe_id"])

	// View page
	env.Get(t, "/apps/recipes/recipe/"+id, alice.Auth).
		Status(200).
		Contains("My Recipe").
		Contains("Mix ingredients").
		Contains("1 hr 30 min").
		Contains("45 min").
		Contains("410 kcal")

	// Edit page
	env.Get(t, "/apps/recipes/recipe/"+id+"/edit", alice.Auth).
		Status(200).
		Contains("Edit recipe").
		Contains(`name="prep_h" value="1"`).
		Contains(`name="prep_m" value="30"`)

	// Update via form
	postFormAction(t, env, "/apps/recipes/recipes/"+id, url.Values{
		"name":         {"Updated Recipe"},
		"description":  {"Updated description"},
		"category":     {"Desserts"},
		"ingredients":  {"2 cups flour"},
		"instructions": {"Mix well"},
		"keywords":     {"baking"},
	}, cookie, csrf).Status(303)

	// Verify update
	env.Get(t, "/apps/recipes/recipe/"+id, alice.Auth).
		Status(200).
		Contains("Updated Recipe")

	// Delete
	deleteAction(t, env, "/apps/recipes/recipes/"+id, cookie, csrf).
		Status(200)

	// Verify deleted
	env.Get(t, "/apps/recipes/recipe/"+id, alice.Auth).Status(404)
}

func TestRecipesNewPage(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)

	env.Get(t, "/apps/recipes/new", alice.Auth).
		Status(200).
		Contains("New recipe")
}

func TestRecipesImportPage(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)

	env.Get(t, "/apps/recipes/import", alice.Auth).
		Status(200).
		Contains("Import recipe")
}

func TestRecipesCategoryPage(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	// Create recipe with category
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"Chicken Soup","recipeCategory":"Soups"}`),
		cookie, csrf).Status(200)

	env.Get(t, "/apps/recipes/category/Soups", alice.Auth).
		Status(200).
		Contains("Soups").
		Contains("Chicken Soup")
}

func TestRecipesKeywordPage(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"Quick Salad","keywords":"quick, healthy"}`),
		cookie, csrf).Status(200)

	env.Get(t, "/apps/recipes/keyword/quick", alice.Auth).
		Status(200).
		Contains("quick").
		Contains("Quick Salad")
}

func TestRecipesImportAPI(t *testing.T) {
	// Serve a fake recipe page with JSON-LD
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
			<script type="application/ld+json">
			{
				"@context": "https://schema.org",
				"@type": "Recipe",
				"name": "Imported Soup",
				"description": "Imported from the web",
				"recipeCategory": "Soups",
				"recipeIngredient": ["water", "salt"],
				"recipeInstructions": [{"@type": "HowToStep", "text": "Boil water"}],
				"keywords": "soup, easy"
			}
			</script>
			</head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	cookie, csrf := sessionAuth(t, env, alice)

	var imported struct {
		ID int64 `json:"id"`
	}
	postAction(t, env, "/apps/recipes/api/import",
		map[string]string{"url": srv.URL}, cookie, csrf).
		Status(200).JSON(&imported)
	require.True(t, imported.ID > 0)

	// Verify the imported recipe
	var got map[string]any
	env.Get(t, "/apps/recipes/api/recipes/"+fmt.Sprintf("%d", imported.ID), alice.Auth).
		Status(200).JSON(&got)
	require.Equal(t, "Imported Soup", got["name"])
}

func TestRecipesImportFromURLForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
			<script type="application/ld+json">
			{"@type":"Recipe","name":"Form Import"}
			</script>
			</head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	postFormAction(t, env, "/apps/recipes/recipes/import-url", url.Values{
		"url": {srv.URL},
	}, cookie, csrf).Status(303)

	// Verify recipe was imported
	env.Get(t, "/apps/recipes/", alice.Auth).
		Status(200).
		Contains("Form Import")
}

func TestRecipesCreateValidation(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	// Missing name via form: back to the form (see TestRecipesFormRejectionKeepsInput).
	postFormAction(t, env, "/apps/recipes/recipes", url.Values{
		"description": {"no name given"},
	}, cookie, csrf).Status(303)

	// Missing name via API
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe"}`),
		cookie, csrf).Status(400)

	// Invalid JSON via API
	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/apps/recipes/api/recipes",
		strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	env.Do(t, req, cookie, csrf).Status(400)
}

// flashCookie returns the flash cookie a response set, as a request option.
func flashCookie(t testing.TB, resp *cogtest.Response) cogtest.HTTPOpt {
	t.Helper()
	for _, ck := range resp.Cookies() {
		if ck.Name == cogplugin.FlashCookieName {
			return cogtest.Header("Cookie", ck.Name+"="+ck.Value)
		}
	}
	t.Fatal("response set no flash cookie")
	return nil
}

// A save the form rejects goes back to the form with the reason and with
// everything the user typed, rather than an error page that loses the input
// and offers to re-submit on reload. The method is long enough that it would
// not fit in the flash cookie.
func TestRecipesFormRejectionKeepsInput(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	method := strings.Repeat("Stir the pot and wait patiently. ", 300)
	typed := url.Values{
		"name":         {"   "},
		"description":  {"Grandma's <best> soup"},
		"ingredients":  {"1 onion\n2 carrots"},
		"instructions": {method},
		"keywords":     {"soup, winter"},
		"prep_h":       {"1"},
		"cook_m":       {"40"},
	}

	resp := postFormAction(t, env, "/apps/recipes/recipes", typed, cookie, csrf)
	resp.Status(303)
	require.Equal(t, "/apps/recipes/new", resp.Header.Get("Location"))
	flash := flashCookie(t, resp)

	env.Get(t, "/apps/recipes/new", alice.Auth, flash).
		Status(200).
		Contains("The recipe was not saved: recipe name is required.").
		Contains("Grandma&#39;s &lt;best&gt; soup").
		Contains("1 onion\n2 carrots").
		Contains(method).
		Contains(`value="soup, winter"`).
		Contains(`name="prep_h" value="1"`).
		Contains(`name="cook_m" value="40"`)

	// The draft is taken by the page that shows it: a reload is a blank form.
	env.Get(t, "/apps/recipes/new", alice.Auth, flash).
		Status(200).
		NotContains("Grandma")

	var list []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 0, len(list))

	// Editing: a bad time sends the edit form back with what was typed, and
	// the stored recipe is untouched.
	postFormAction(t, env, "/apps/recipes/recipes", url.Values{"name": {"Soup"}}, cookie, csrf).Status(303)
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 1, len(list))
	id := fmt.Sprintf("%.0f", list[0]["recipe_id"])

	resp = postFormAction(t, env, "/apps/recipes/recipes/"+id, url.Values{
		"name":   {"Better soup"},
		"prep_m": {"soon"},
	}, cookie, csrf)
	resp.Status(303)
	require.Equal(t, "/apps/recipes/recipe/"+id+"/edit", resp.Header.Get("Location"))
	env.Get(t, "/apps/recipes/recipe/"+id+"/edit", alice.Auth, flashCookie(t, resp)).
		Status(200).
		Contains("is not a whole number").
		Contains(`value="Better soup"`).
		Contains(`name="prep_m" value="soon"`)
	env.Get(t, "/apps/recipes/recipe/"+id, alice.Auth).
		Status(200).
		Contains("Soup").
		NotContains("Better soup")
}

func TestRecipesImportValidation(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	// Empty URL via API
	postAction(t, env, "/apps/recipes/api/import",
		map[string]string{"url": ""}, cookie, csrf).Status(400)

	// Empty URL via form: back to the form with the reason, not an error page.
	resp := postFormAction(t, env, "/apps/recipes/recipes/import-url", url.Values{
		"url": {""},
	}, cookie, csrf)
	resp.Status(303)
	require.Equal(t, "/apps/recipes/import", resp.Header.Get("Location"))

	// Invalid scheme
	postAction(t, env, "/apps/recipes/api/import",
		map[string]string{"url": "ftp://evil.com"}, cookie, csrf).Status(400)
}

func TestRecipesUserIsolation(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	aliceCookie, aliceCSRF := sessionAuth(t, env, alice)

	// Alice creates a recipe
	postAction(t, env, "/apps/recipes/api/recipes",
		json.RawMessage(`{"@type":"Recipe","name":"Alice Only"}`),
		aliceCookie, aliceCSRF).Status(200)

	// Bob sees empty list
	var bobList []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", bob.Auth).
		Status(200).JSON(&bobList)
	require.Equal(t, 0, len(bobList))

	// Alice sees her recipe
	var aliceList []map[string]any
	env.Get(t, "/apps/recipes/api/recipes", alice.Auth).
		Status(200).JSON(&aliceList)
	require.Equal(t, 1, len(aliceList))
}

// A page that is not a recipe has to say so on the import form. Answering the
// POST with an error page dropped the layout and left the browser offering to
// re-submit on reload.
func TestRecipesImportFailureReturnsToTheForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><p>an article, not a recipe</p></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	resp := postFormAction(t, env, "/apps/recipes/recipes/import-url", url.Values{
		"url": {srv.URL},
	}, cookie, csrf)
	resp.Status(303)
	require.Equal(t, "/apps/recipes/import", resp.Header.Get("Location"))

	var flash *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cogplugin.FlashCookieName {
			flash = ck
		}
	}
	require.NotNil(t, flash)

	// The form comes back with the reason on it, inside the page.
	env.Get(t, "/apps/recipes/import", alice.Auth,
		cogtest.Header("Cookie", flash.Name+"="+flash.Value)).
		Status(200).
		Contains("Could not import that page").
		Contains("Recipe URL")
}

// Cloudflare challenges a share of requests on fingerprints no header can
// change, so a 403 is a coin toss rather than a refusal. Retrying it is the
// only thing that gets the import through.
func TestRecipesImportRetriesABotWallChallenge(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
			<script type="application/ld+json">
			{"@type":"Recipe","name":"Challenged Soup"}
			</script>
			</head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	env.PostForm(t, "/apps/recipes/recipes/import-url", url.Values{"url": {srv.URL}},
		cookie, csrf, cogtest.StopRedirect).
		Status(303)

	require.Equal(t, 2, attempts)
}

// Retrying is bounded, and what comes back names the wall rather than a bare
// status code — the person importing can only act on the former.
func TestRecipesImportGivesUpOnAPersistentBotWall(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	resp := env.PostForm(t, "/apps/recipes/recipes/import-url", url.Values{"url": {srv.URL}},
		cookie, csrf, cogtest.StopRedirect).
		Status(303)

	require.Equal(t, 3, attempts)

	var flash *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cogplugin.FlashCookieName {
			flash = ck
		}
	}
	require.NotNil(t, flash)
	env.Get(t, "/apps/recipes/import", alice.Auth,
		cogtest.Header("Cookie", flash.Name+"="+flash.Value)).
		Status(200).
		Contains("bot protection")
}

// Recipe sites serve a 403 to anything that does not look like a browser. The
// person importing is using one, so its User-Agent is what goes out.
func TestRecipesImportForwardsTheRequestersUserAgent(t *testing.T) {
	const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/141.0 Safari/537.36"

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.UserAgent())
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
			<script type="application/ld+json">
			{"@type":"Recipe","name":"UA Soup"}
			</script>
			</head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	env.PostForm(t, "/apps/recipes/recipes/import-url", url.Values{"url": {srv.URL}},
		cookie, csrf, cogtest.Header("User-Agent", browserUA), cogtest.StopRedirect).
		Status(303)

	require.Equal(t, 1, len(seen))
	require.Equal(t, browserUA, seen[0])
}

// A User-Agent is caller-controlled and goes to a third party. One that could
// not legally sit in a header is replaced, not repaired.
func TestRecipesImportRejectsAnUnusableUserAgent(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.UserAgent()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
			<script type="application/ld+json">
			{"@type":"Recipe","name":"Fallback Soup"}
			</script>
			</head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	env := cogtest.New(t, recipes.New())
	alice := env.CreateUser(t, "alice", "pass")
	ensureMigrated(t, env, alice.Username)
	cookie, csrf := sessionAuth(t, env, alice)

	// The API path takes the same route to the fetch, and a JSON client is
	// where an odd User-Agent would come from.
	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/apps/recipes/api/import",
		strings.NewReader(`{"url":"`+srv.URL+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "curl/8.0 ééé"+strings.Repeat("x", 300))
	env.Do(t, req, cookie, csrf).Status(200)

	require.Equal(t, "Cog/1.0 (Recipe Importer)", seen)
}
