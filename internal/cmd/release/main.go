// Release tool publishes the list of releases the updater reads.
//
//	go tool release keygen -out release-key
//	    Make a signing key: the private half goes to release-key (and the
//	    COG_RELEASE_KEY secret), the public half is printed for
//	    internal/update/keys.go.
//	go tool release record -version v1.2.3 -out dist/release.json dist/cog-*
//	    Sign the record of a release the job just built: each binary's
//	    checksum and size, made from the files themselves.
//	go tool release manifest -repo owner/name -out public/releases.json
//	    List the repository's GitHub releases and sign the list. Each
//	    binary's checksum comes from its release's signed record, never
//	    from what is attached to the release now: a release without a
//	    record is left out, and one whose record or binaries do not check
//	    out stops it.
//	go tool release sums -out dist/SHA256SUMS dist/cog-*
//	    Write the checksums of a release's binaries, for people.
//
// record and manifest sign with the key in COG_RELEASE_KEY, which only the
// release workflow, run for a tag, has. GITHUB_TOKEN, if set, raises
// GitHub's rate limit.
package main

import (
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
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/update"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: go tool release keygen|record|manifest|sums [flags]")
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "sums":
		err = sums(os.Args[2:])
	case "record":
		err = recordCmd(os.Args[2:])
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
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
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
		var recURL, sigURL string
		for _, a := range r.Assets {
			switch a.Name {
			case recordName:
				recURL = a.URL
			case recordName + update.SignatureSuffix:
				sigURL = a.URL
			}
		}
		if recURL == "" || sigURL == "" {
			log.Printf("%s: no signed %s, left out", r.TagName, recordName)
			continue
		}
		rec, err := get(ctx, recURL)
		if err != nil {
			return fmt.Errorf("%s: %w", r.TagName, err)
		}
		sig, err := get(ctx, sigURL)
		if err != nil {
			return fmt.Errorf("%s: %w", r.TagName, err)
		}
		rel, err := release(r, rec, sig, update.TrustedKeys)
		if err != nil {
			return fmt.Errorf("%s: %w", r.TagName, err)
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

// recordName is the release asset that holds a release's signed record.
const recordName = "release.json"

// record is what the release workflow signs for a release, from the
// binaries it built: what each one must hash to, and its size.
type record struct {
	Version string                 `json:"version"`
	Assets  map[string]recordAsset `json:"assets"` // by platform, "linux-amd64"
}

type recordAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func recordCmd(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	ver := fs.String("version", os.Getenv("GITHUB_REF_NAME"), "the release's version, its tag")
	out := fs.String("out", "dist/"+recordName, "file to write; the signature goes beside it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ver == "" {
		return errors.New("record: no -version")
	}
	key, err := privateKey()
	if err != nil {
		return err
	}
	rec, err := makeRecord(*ver, fs.Args())
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(*out+update.SignatureSuffix, update.Sign(key, data), 0o644); err != nil {
		return err
	}
	log.Printf("signed %s for %s: %d binaries", *out, *ver, len(rec.Assets))
	return nil
}

// makeRecord is the record of release version, made from its binaries.
func makeRecord(version string, paths []string) (record, error) {
	rec := record{Version: version, Assets: map[string]recordAsset{}}
	for _, path := range paths {
		name := filepath.Base(path)
		match := assetName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return record{}, err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return record{}, fmt.Errorf("%s: %w", path, err)
		}
		rec.Assets[match[1]] = recordAsset{Name: name, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	}
	if len(rec.Assets) == 0 {
		return record{}, errors.New("record: no binaries (cog-<os>-<arch>)")
	}
	return rec, nil
}

// release is r as the manifest lists it: its binaries as its signed
// record (rec, signed sig) has them, each found among r's assets by name
// and size.
func release(r ghRelease, rec, sig []byte, keys []ed25519.PublicKey) (update.Release, error) {
	if err := update.Verify(keys, rec, sig); err != nil {
		return update.Release{}, fmt.Errorf("%s: %w", recordName, err)
	}
	var signed record
	if err := json.Unmarshal(rec, &signed); err != nil {
		return update.Release{}, fmt.Errorf("%s: %w", recordName, err)
	}
	if signed.Version != r.TagName {
		return update.Release{}, fmt.Errorf("%s is the record of %q", recordName, signed.Version)
	}
	rel := update.Release{Version: r.TagName, Date: r.PublishedAt, NotesURL: r.HTMLURL, Prerelease: r.Prerelease, Assets: map[string]update.Asset{}}
	for platform, a := range signed.Assets {
		i := slices.IndexFunc(r.Assets, func(g ghAsset) bool { return g.Name == a.Name })
		if i < 0 {
			return update.Release{}, fmt.Errorf("%s is not attached", a.Name)
		}
		if r.Assets[i].Size != a.Size {
			return update.Release{}, fmt.Errorf("%s is %d bytes, not the %d it was built as", a.Name, r.Assets[i].Size, a.Size)
		}
		rel.Assets[platform] = update.Asset{URL: r.Assets[i].URL, SHA256: a.SHA256, Size: a.Size}
	}
	return rel, nil
}

func get(ctx context.Context, url string) ([]byte, error) {
	res, err := request(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return b, nil
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
