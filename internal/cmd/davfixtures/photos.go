package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// --- photos ---

// photoTypes maps a sniffed content type to vCard 3.0's TYPE and 4.0's
// media type.
var photoTypes = map[string]string{"image/jpeg": "JPEG", "image/png": "PNG", "image/gif": "GIF"}

// runPhotos puts the photos the old contacts web UI kept beside each card
// (cards.photo_path, a file under the data directory) into the cards of an
// exported address book file, so the import carries them: no export over
// CardDAV does, since the old server never wrote them into the card.
func runPhotos(args []string) error {
	fs := flag.NewFlagSet("photos", flag.ExitOnError)
	dbPath := fs.String("db", "", "the old server's contacts database (<data>/<user>/app_data/contacts/plugin.db)")
	data := fs.String("data", "", "where the old server's data directory is now; photo paths under -prefix are read from here")
	prefix := fs.String("prefix", "/data", "the old server's data directory, as photo paths name it")
	vcf := fs.String("vcf", "", "the address book file from export -import, rewritten in place")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" || *data == "" || *vcf == "" {
		return fmt.Errorf("-db, -data and -vcf are required")
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", "file:"+*dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	paths, err := oldPhotoPaths(db)
	if err != nil {
		return err
	}

	in, err := os.ReadFile(*vcf)
	if err != nil {
		return err
	}
	comps, err := contentline.Parse(bytes.NewReader(in))
	if err != nil {
		return fmt.Errorf("%s: %w", *vcf, err)
	}
	added, kept := 0, 0
	for _, c := range comps {
		if !c.Is("VCARD") {
			continue
		}
		p, ok := paths[uidOf(c)]
		if !ok {
			continue
		}
		if c.Prop("PHOTO") != nil {
			kept++ // a client's own photo wins
			continue
		}
		rel, ok := strings.CutPrefix(p, strings.TrimSuffix(*prefix, "/")+"/")
		if !ok {
			return fmt.Errorf("card %s: photo %s is not under %s", uidOf(c), p, *prefix)
		}
		img, err := os.ReadFile(filepath.Join(*data, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("card %s: %w", uidOf(c), err)
		}
		media := http.DetectContentType(img)
		typ, ok := photoTypes[media]
		if !ok {
			return fmt.Errorf("card %s: %s is %s, not an image a card can hold", uidOf(c), p, media)
		}
		photo := contentline.Property{Name: "PHOTO"}
		if v := c.Prop("VERSION"); v != nil && v.Value == "4.0" {
			photo.Value = "data:" + media + ";base64," + base64.StdEncoding.EncodeToString(img)
		} else {
			photo.Params = []contentline.Param{{Name: "ENCODING", Values: []string{"b"}}, {Name: "TYPE", Values: []string{typ}}}
			photo.Value = base64.StdEncoding.EncodeToString(img)
		}
		c.Props = append(c.Props, photo)
		added++
	}
	var out bytes.Buffer
	if err := contentline.Write(&out, comps); err != nil {
		return err
	}
	if err := os.WriteFile(*vcf, out.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Printf("%s: %d photos added, %d cards kept their own, %d cards had a photo file\n", *vcf, added, kept, len(paths))
	return nil
}

// oldPhotoPaths returns each card's photo file by UID, from the contacts
// schema before the DAV storage rewrite.
func oldPhotoPaths(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT uid, photo_path FROM cards WHERE photo_path != ''`)
	if err != nil {
		return nil, fmt.Errorf("read cards: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var uid, p string
		if err := rows.Scan(&uid, &p); err != nil {
			return nil, fmt.Errorf("read cards: %w", err)
		}
		out[uid] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cards: %w", err)
	}
	return out, nil
}
