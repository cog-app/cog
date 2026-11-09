package bookmarks

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"
)

// HashFunctions are the hash functions a folder hash may use, by the names
// NextCloud Bookmarks advertises in its capabilities.
var HashFunctions = []string{"sha256", "murmur3a", "xxh32"}

// HashFields are the bookmark fields a folder hash may cover.
var HashFields = []string{"title", "url", "tags", "description"}

// FolderHash hashes a folder the way NextCloud Bookmarks does, which is the
// way Floccus hashes its copy of the tree: a bookmark is the hash of a JSON
// object of fields, in the order asked for, and a folder the hash of
// {"title": ..., "children": [child hashes in order]}, without a title for the
// root. Floccus compares the two to skip folders that have not changed, so a
// difference costs a sync time rather than correctness.
//
// The tree must be loaded to every level.
func (t *Tree) FolderHash(folder int64, fields []string, hashFn string) (string, error) {
	h, ok := hashers[hashFn]
	if !ok {
		return "", fmt.Errorf("unknown hash function %q", hashFn)
	}
	for _, f := range fields {
		if !slices.Contains(HashFields, f) {
			return "", fmt.Errorf("unknown hash field %q", f)
		}
	}
	return t.hashFolder(folder, fields, h), nil
}

func (t *Tree) hashFolder(folder int64, fields []string, h func(string) string) string {
	var b strings.Builder
	b.WriteByte('{')
	if f := t.folders[folder]; f != nil {
		b.WriteString(`"title":`)
		writeJSONString(&b, f.Title)
		b.WriteByte(',')
	}
	b.WriteString(`"children":[`)
	for i, c := range t.children[folder] {
		if i > 0 {
			b.WriteByte(',')
		}
		if c.Type == ChildFolder {
			writeJSONString(&b, t.hashFolder(c.ID, fields, h))
		} else {
			writeJSONString(&b, hashBookmark(t.bookmarks[c.ID], fields, h))
		}
	}
	b.WriteString("]}")
	return h(b.String())
}

func hashBookmark(bm *Bookmark, fields []string, h func(string) string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, f)
		b.WriteByte(':')
		switch f {
		case "title":
			writeJSONString(&b, bm.Title)
		case "url":
			writeJSONString(&b, bm.URL)
		case "description":
			writeJSONString(&b, bm.Description)
		case "tags":
			// Tags are loaded sorted by byte, as PHP's SORT_STRING sorts them.
			b.WriteByte('[')
			for j, tag := range bm.Tags {
				if j > 0 {
					b.WriteByte(',')
				}
				writeJSONString(&b, tag)
			}
			b.WriteByte(']')
		}
	}
	b.WriteByte('}')
	return h(b.String())
}

// writeJSONString writes s as JSON.stringify and PHP's json_encode with
// JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE both do. encoding/json differs
// from them: it escapes <, >, &, U+2028 and U+2029, and writes \b and \f as
// \u0008 and \u000c, any of which would change the hash.
func writeJSONString(b *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

var hashers = map[string]func(string) string{
	"sha256": func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	},
	// Big-endian hex, as PHP's hash('murmur3a') and Floccus both write it.
	"murmur3a": func(s string) string {
		var out [4]byte
		binary.BigEndian.PutUint32(out[:], murmur3(s))
		return hex.EncodeToString(out[:])
	},
	// Without leading zeros: Floccus writes it with toString(16), so this
	// matches the client rather than PHP, which pads to eight digits.
	"xxh32": func(s string) string {
		return strconv.FormatUint(uint64(xxh32(s)), 16)
	},
}

// murmur3 is 32-bit MurmurHash3 with seed 0.
func murmur3(s string) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	var h uint32
	n := len(s)
	i := 0
	for ; i+4 <= n; i += 4 {
		k := uint32(s[i]) | uint32(s[i+1])<<8 | uint32(s[i+2])<<16 | uint32(s[i+3])<<24
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
		h = bits.RotateLeft32(h, 13)
		h = h*5 + 0xe6546b64
	}
	var k uint32
	switch n - i {
	case 3:
		k ^= uint32(s[i+2]) << 16
		fallthrough
	case 2:
		k ^= uint32(s[i+1]) << 8
		fallthrough
	case 1:
		k ^= uint32(s[i])
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// xxh32 is 32-bit xxHash with seed 0.
func xxh32(s string) uint32 {
	const (
		p1 uint32 = 2654435761
		p2 uint32 = 2246822519
		p3 uint32 = 3266489917
		p4 uint32 = 668265263
		p5 uint32 = 374761393
	)
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32([]byte(s[i : i+4])) }
	round := func(acc, in uint32) uint32 {
		acc += in * p2
		acc = bits.RotateLeft32(acc, 13)
		return acc * p1
	}
	n := len(s)
	i := 0
	var h uint32
	if n >= 16 {
		// Variables, so the sums wrap as the algorithm intends rather than
		// overflowing as constants.
		a, b := p1, p2
		v1, v2, v3, v4 := a+b, b, uint32(0), -a
		for ; i+16 <= n; i += 16 {
			v1 = round(v1, u32(i))
			v2 = round(v2, u32(i+4))
			v3 = round(v3, u32(i+8))
			v4 = round(v4, u32(i+12))
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) + bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = p5
	}
	h += uint32(n)
	for ; i+4 <= n; i += 4 {
		h += u32(i) * p3
		h = bits.RotateLeft32(h, 17) * p4
	}
	for ; i < n; i++ {
		h += uint32(s[i]) * p5
		h = bits.RotateLeft32(h, 11) * p1
	}
	h ^= h >> 15
	h *= p2
	h ^= h >> 13
	h *= p3
	h ^= h >> 16
	return h
}
