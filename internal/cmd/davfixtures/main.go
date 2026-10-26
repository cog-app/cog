// Davfixtures maintains the local, uncommitted parts of the DAV fixture
// corpus (testdata/dav/README.md). Usage:
//
//	go tool davfixtures gpl [-out tmp/dav-fixtures-gpl]
//	go tool davfixtures export [-base https://cog.duckfam.us] [-out tmp/dav-export]
//	go tool davfixtures anonymise -out dir -prefix name file...
//
// gpl fetches copyleft-licensed client output, pinned by commit in
// gpl-manifest.txt, which may be tested against locally but not committed.
//
// export downloads every calendar and address book resource from a DAV
// server, byte for byte. Credentials come from COG_USER and COG_PASS so they
// never appear on a command line:
//
//	COG_USER=jonathan COG_PASS="$(pass show cog.duckfam.us/jonathan | head -n1)" go tool davfixtures export
//
// anonymise rewrites personal text in exported files with placeholders,
// keeping structure, parameters, times and X- properties, so the result can be
// committed under testdata/dav/*/cog-production. Check its output by eye
// before committing: it only knows the properties listed in textProps.
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

//go:embed gpl-manifest.txt
var gplManifest string

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: davfixtures gpl|export|anonymise [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gpl":
		err = runGPL(os.Args[2:])
	case "export":
		err = runExport(os.Args[2:])
	case "anonymise":
		err = runAnonymise(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "davfixtures:", err)
		os.Exit(1)
	}
}

// --- gpl ---

// runGPL fetches each manifest line ("repo-url commit upstream-path
// dest-path") with a shallow fetch of the pinned commit, so no checkout or
// build of untrusted code happens.
func runGPL(args []string) error {
	fs := flag.NewFlagSet("gpl", flag.ExitOnError)
	out := fs.String("out", "tmp/dav-fixtures-gpl", "output directory")
	src := fs.String("src", "tmp/fixture-src-gpl", "directory for fetched repositories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fetched := map[string]string{} // url@commit -> git dir
	sc := bufio.NewScanner(strings.NewReader(gplManifest))
	n := 0
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 4 {
			return fmt.Errorf("bad manifest line %q", sc.Text())
		}
		repo, commit, upstream, dest := f[0], f[1], f[2], f[3]
		key := repo + "@" + commit
		dir, ok := fetched[key]
		if !ok {
			dir = filepath.Join(*src, strings.TrimSuffix(path.Base(repo), ".git")+"-"+commit[:8])
			if err := fetchCommit(dir, repo, commit); err != nil {
				return fmt.Errorf("%s: %w", repo, err)
			}
			fetched[key] = dir
		}
		blob, err := exec.Command("git", "-C", dir, "cat-file", "blob", commit+":"+upstream).Output()
		if err != nil {
			return fmt.Errorf("%s:%s: %w", repo, upstream, err)
		}
		target := filepath.Join(*out, dest)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, blob, 0o644); err != nil {
			return err
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	fmt.Printf("%d files in %s\n", n, *out)
	return nil
}

func fetchCommit(dir, repo, commit string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if exec.Command("git", "-C", dir, "cat-file", "-e", commit).Run() == nil {
			return nil
		}
	} else if err := runGit("", "init", "-q", dir); err != nil {
		return err
	}
	return runGit(dir, "fetch", "-q", "--depth", "1", repo, commit)
}

func runGit(dir string, args ...string) error {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// --- export ---

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Prop struct {
				ResourceType struct {
					Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
					AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

type davClient struct{ base, user, pass string }

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	base := fs.String("base", "https://cog.duckfam.us", "server base URL")
	out := fs.String("out", "tmp/dav-export", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := &davClient{base: *base, user: os.Getenv("COG_USER"), pass: os.Getenv("COG_PASS")}
	if c.user == "" || c.pass == "" {
		return fmt.Errorf("COG_USER and COG_PASS are required")
	}
	for _, home := range []struct{ kind, ext string }{{"calendars", ".ics"}, {"addressbooks", ".vcf"}} {
		if err := c.exportHome(*out, home.kind, "/remote.php/dav/"+home.kind+"/"+c.user+"/", home.ext); err != nil {
			return fmt.Errorf("%s: %w", home.kind, err)
		}
	}
	return nil
}

func (c *davClient) do(method, p, depth, body string) ([]byte, error) {
	req, err := http.NewRequest(method, c.base+p, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s", method, p, resp.Status)
	}
	return b, nil
}

func (c *davClient) propfind(href, prop string) (*multistatus, error) {
	b, err := c.do("PROPFIND", href, "1", `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:`+prop+`/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	var ms multistatus
	if err := xml.Unmarshal(b, &ms); err != nil {
		return nil, fmt.Errorf("parse %s: %w", href, err)
	}
	return &ms, nil
}

func (c *davClient) exportHome(out, kind, home, ext string) error {
	ms, err := c.propfind(home, "resourcetype")
	if err != nil {
		return err
	}
	for _, r := range ms.Responses {
		isColl := false
		for _, ps := range r.Propstat {
			isColl = isColl || ps.Prop.ResourceType.Calendar != nil || ps.Prop.ResourceType.AddressBook != nil
		}
		if !isColl {
			continue
		}
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			return err
		}
		dir := filepath.Join(out, kind, strings.Trim(strings.TrimPrefix(href, "/remote.php/dav/"+kind+"/"), "/"))
		if err := c.exportCollection(r.Href, dir, ext); err != nil {
			return fmt.Errorf("%s: %w", href, err)
		}
	}
	return nil
}

// exportCollection lists with PROPFIND, then GETs each resource: XML parsers
// normalise CRLF, and the point is the exact bytes.
func (c *davClient) exportCollection(href, dir, ext string) error {
	ms, err := c.propfind(href, "getetag")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	n := 0
	for _, r := range ms.Responses {
		if strings.HasSuffix(r.Href, "/") {
			continue
		}
		data, err := c.do("GET", r.Href, "", "")
		if err != nil {
			return err
		}
		name, err := url.PathUnescape(path.Base(r.Href))
		if err != nil {
			return err
		}
		if !strings.HasSuffix(name, ext) {
			name += ext
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), data, 0o600); err != nil {
			return err
		}
		n++
	}
	fmt.Printf("%s: %d resources\n", dir, n)
	return nil
}

// --- anonymise ---

var textProps = map[string]string{
	"SUMMARY": "Event", "DESCRIPTION": "Description", "LOCATION": "Place", "COMMENT": "Comment",
	"FN": "Person", "NICKNAME": "Nick", "NOTE": "Note", "ORG": "Org", "TITLE": "Title",
}

func runAnonymise(args []string) error {
	fs := flag.NewFlagSet("anonymise", flag.ExitOnError)
	out := fs.String("out", "", "output directory")
	prefix := fs.String("prefix", "item", "output file name prefix")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	var a anonymiser
	for i, p := range fs.Args() {
		in, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		comps, err := contentline.Parse(bytes.NewReader(in))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		for _, c := range comps {
			a.scrub(c)
		}
		var b bytes.Buffer
		if err := contentline.Write(&b, comps); err != nil {
			return err
		}
		name := fmt.Sprintf("%s-%02d%s", *prefix, i+1, filepath.Ext(p))
		if err := os.WriteFile(filepath.Join(*out, name), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type anonymiser struct{ n int }

func (a *anonymiser) next() int { a.n++; return a.n }

func (a *anonymiser) scrub(c *contentline.Component) {
	for i := range c.Props {
		p := &c.Props[i]
		if p.Malformed() {
			// Keep it malformed (no colon) but drop its text.
			p.Value = fmt.Sprintf(`\nMalformed continuation %d`, a.next())
			continue
		}
		name := strings.ToUpper(p.Name)
		if base, ok := textProps[name]; ok {
			p.Value = fmt.Sprintf("%s %d", base, a.next())
			if name == "DESCRIPTION" {
				p.Value += `\nSecond line`
			}
		}
		switch name {
		case "N":
			n := a.next()
			p.Value = fmt.Sprintf("Family%d;Given%d;;;", n, n)
		case "UID":
			if c.Is("VCARD") {
				p.Value = fmt.Sprintf("00000000-0000-4000-8000-%012d", a.next())
			}
		case "EMAIL":
			p.Value = fmt.Sprintf("person%d@example.com", a.next())
		case "TEL":
			p.Value = fmt.Sprintf("+1-555-01%02d", a.next()%100)
		case "ADR":
			p.Value = fmt.Sprintf(";;%d Example St;Springfield;ST;00000;", a.next())
		case "ATTENDEE", "ORGANIZER":
			p.Value = fmt.Sprintf("mailto:person%d@example.com", a.next())
		}
		for j := range p.Params {
			if strings.EqualFold(p.Params[j].Name, "CN") {
				p.Params[j].Values = []string{fmt.Sprintf("Person %d", a.n)}
			}
		}
	}
	for _, sub := range c.Components {
		a.scrub(sub)
	}
}
