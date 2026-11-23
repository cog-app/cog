// Release tool publishes the list of releases the updater reads.
//
//	go tool release keygen -out release-key
//	    Make a signing key: the private half goes to release-key (and the
//	    COG_RELEASE_KEY secret), the public half is printed for
//	    internal/update/keys.go.
//	go tool release manifest -repo owner/name -out public/releases.json
//	    List the repository's GitHub releases, with each binary's checksum
//	    from the release's SHA256SUMS, and sign the list with the key in
//	    COG_RELEASE_KEY. GITHUB_TOKEN, if set, raises GitHub's rate limit.
//	go tool release sums -out dist/SHA256SUMS dist/cog-*
//	    Write the checksums of a release's binaries.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/update"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: go tool release keygen|manifest|sums [flags]")
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "sums":
		err = sums(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "release-key", "file for the private key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

// privateKey is the signing key in COG_RELEASE_KEY, as keygen wrote it.
func privateKey() (ed25519.PrivateKey, error) {
	v := strings.TrimSpace(os.Getenv("COG_RELEASE_KEY"))
	if v == "" {
		return nil, errors.New("COG_RELEASE_KEY is not set")
	}
	seed, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("COG_RELEASE_KEY is not a key from go tool release keygen")
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	for _, k := range update.TrustedKeys {
		if k.Equal(pub) {
			return key, nil
		}
	}
	return nil, errors.New("COG_RELEASE_KEY is not one of internal/update.TrustedKeys: builds would refuse its signature")
}

func sums(args []string) error {
	fs := flag.NewFlagSet("sums", flag.ExitOnError)
	out := fs.String("out", "SHA256SUMS", "file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var b strings.Builder
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(&b, "%x  %s\n", h.Sum(nil), filepath.Base(path))
	}
	return os.WriteFile(*out, []byte(b.String()), 0o644)
}

// ghRelease is the part of GitHub's release object the manifest needs.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// assetName is a release binary: cog-linux-amd64, cog-windows-amd64.exe.
var assetName = regexp.MustCompile(`^cog-([a-z0-9]+-[a-z0-9]+)(\.exe)?$`)

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub repository, owner/name")
	out := fs.String("out", "public/releases.json", "file to write; the signature goes beside it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := privateKey()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var all []ghRelease
	for page := 1; ; page++ {
		var batch []ghRelease
		if err := getJSON(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", *repo, page), &batch); err != nil {
			return err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	var m update.Manifest
	for _, r := range all {
		if r.Draft {
			continue
		}
		rel := update.Release{Version: r.TagName, Date: r.PublishedAt, NotesURL: r.HTMLURL, Prerelease: r.Prerelease, Assets: map[string]update.Asset{}}
		var sumsURL string
		for _, a := range r.Assets {
			if a.Name == "SHA256SUMS" {
				sumsURL = a.URL
			}
		}
		if sumsURL == "" {
			log.Printf("%s: no SHA256SUMS, left out", r.TagName)
			continue
		}
		checksums, err := getSums(ctx, sumsURL)
		if err != nil {
			return fmt.Errorf("%s: %w", r.TagName, err)
		}
		for _, a := range r.Assets {
			match := assetName.FindStringSubmatch(a.Name)
			if match == nil {
				continue
			}
			sum, ok := checksums[a.Name]
			if !ok {
				return fmt.Errorf("%s: %s is not in SHA256SUMS", r.TagName, a.Name)
			}
			rel.Assets[match[1]] = update.Asset{URL: a.URL, SHA256: sum, Size: a.Size}
		}
		m.Releases = append(m.Releases, rel)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(*out+update.SignatureSuffix, update.Sign(key, data), 0o644); err != nil {
		return err
	}
	log.Printf("wrote %s with %d releases", *out, len(m.Releases))
	return nil
}

func request(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return res, nil
}

func getJSON(ctx context.Context, url string, v any) error {
	res, err := request(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(v)
}

func getSums(ctx context.Context, url string) (map[string]string, error) {
	res, err := request(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			return nil, fmt.Errorf("SHA256SUMS: %q is not a checksum", f[0])
		}
		sums[strings.TrimPrefix(f[1], "*")] = f[0]
	}
	return sums, sc.Err()
}
