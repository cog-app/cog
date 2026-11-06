package ssh

import (
	"encoding/binary"
	"io"
	"io/fs"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/vfs"
)

// --- parseSCPArgs ---

func TestParseSCPArgs_Sink(t *testing.T) {
	a := parseSCPArgs("scp -t /home/user/files/out.txt")
	require.True(t, a.sink)
	require.False(t, a.source)
	require.False(t, a.recursive)
	require.Equal(t, "/home/user/files/out.txt", a.path)
}

func TestParseSCPArgs_Source(t *testing.T) {
	a := parseSCPArgs("scp -f /home/user/files/in.txt")
	require.False(t, a.sink)
	require.True(t, a.source)
	require.Equal(t, "/home/user/files/in.txt", a.path)
}

func TestParseSCPArgs_RecursiveSink(t *testing.T) {
	a := parseSCPArgs("scp -r -t /home/user/files/dir")
	require.True(t, a.sink)
	require.True(t, a.recursive)
}

func TestParseSCPArgs_CombinedFlags(t *testing.T) {
	a := parseSCPArgs("scp -rt /home/user/files/dir")
	require.True(t, a.sink)
	require.True(t, a.recursive)

	b := parseSCPArgs("scp -rf /home/user/files/dir")
	require.True(t, b.source)
	require.True(t, b.recursive)
}

func TestParseSCPArgs_UnknownFlagIgnored(t *testing.T) {
	a := parseSCPArgs("scp -x -t /a/b")
	require.True(t, a.sink)
}

// --- parsePtyReq ---

func TestParsePtyReq_StandardPayload(t *testing.T) {
	// Construct: 4-byte length + term + 4-byte width + 4-byte height + rest.
	term := "xterm-256color"
	payload := make([]byte, 4+len(term)+4+4+4+4)
	binary.BigEndian.PutUint32(payload[:4], uint32(len(term)))
	copy(payload[4:], term)
	off := 4 + len(term)
	binary.BigEndian.PutUint32(payload[off:off+4], 132)  // width
	binary.BigEndian.PutUint32(payload[off+4:off+8], 43) // height
	// pixel width/height after — not parsed.

	w, h := parsePtyReq(payload)
	require.Equal(t, 132, w)
	require.Equal(t, 43, h)
}

func TestParsePtyReq_ShortPayload(t *testing.T) {
	w, h := parsePtyReq([]byte{1, 2, 3})
	require.Equal(t, 80, w)
	require.Equal(t, 24, h)
}

func TestParsePtyReq_TruncatedAfterTerm(t *testing.T) {
	term := "xterm"
	payload := make([]byte, 4+len(term)+4) // missing height + remainder
	binary.BigEndian.PutUint32(payload[:4], uint32(len(term)))
	copy(payload[4:], term)
	binary.BigEndian.PutUint32(payload[4+len(term):], 100)

	w, h := parsePtyReq(payload)
	require.Equal(t, 80, w)
	require.Equal(t, 24, h)
}

// --- parseStringPayload ---

func TestParseStringPayload_Happy(t *testing.T) {
	payload := make([]byte, 4+5)
	binary.BigEndian.PutUint32(payload[:4], 5)
	copy(payload[4:], "hello")
	require.Equal(t, "hello", parseStringPayload(payload))
}

func TestParseStringPayload_Empty(t *testing.T) {
	payload := []byte{0, 0, 0, 0}
	require.Equal(t, "", parseStringPayload(payload))
}

func TestParseStringPayload_TooShort(t *testing.T) {
	require.Equal(t, "", parseStringPayload([]byte{1, 2}))
}

func TestParseStringPayload_LengthOverflow(t *testing.T) {
	payload := make([]byte, 4+3)
	binary.BigEndian.PutUint32(payload[:4], 100) // claims 100 bytes after
	copy(payload[4:], "abc")
	require.Equal(t, "", parseStringPayload(payload))
}

// --- readerAtBytes ---

func TestReaderAtBytes_ReadAt(t *testing.T) {
	r := readerAtBytes([]byte("hello world"))
	buf := make([]byte, 5)
	n, err := r.ReadAt(buf, 6)
	require.True(t, err == io.EOF)
	require.Equal(t, 5, n)
	require.Equal(t, "world", string(buf))
}

func TestReaderAtBytes_OffsetPastEnd(t *testing.T) {
	r := readerAtBytes([]byte("abc"))
	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 10)
	require.Equal(t, 0, n)
	require.True(t, err == io.EOF)
}

func TestReaderAtBytes_ReadAtMidRange(t *testing.T) {
	r := readerAtBytes([]byte("abcdef"))
	buf := make([]byte, 2)
	n, err := r.ReadAt(buf, 1)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, "bc", string(buf))
}

// --- syntheticInfo ---

func TestSyntheticInfo_File(t *testing.T) {
	fi := &vfs.FileInfo{
		Name:    "doc.txt",
		Size:    42,
		ModTime: time.Unix(1700000000, 0),
		IsDir:   false,
	}
	s := &syntheticInfo{fi: fi}
	require.Equal(t, "doc.txt", s.Name())
	require.Equal(t, int64(42), s.Size())
	require.False(t, s.IsDir())
	require.True(t, s.Sys() == nil)
	require.Equal(t, fs.FileMode(0644), s.Mode())
	require.Equal(t, int64(1700000000), s.ModTime().Unix())
}

func TestSyntheticInfo_Directory(t *testing.T) {
	s := &syntheticInfo{fi: &vfs.FileInfo{Name: "d", IsDir: true}}
	require.True(t, s.IsDir())
	if !s.Mode().IsDir() {
		t.Fatalf("expected dir mode, got %v", s.Mode())
	}
}

// --- listAt ---

func TestListAt_CopiesEntries(t *testing.T) {
	src := []fs.FileInfo{
		&syntheticInfo{fi: &vfs.FileInfo{Name: "a"}},
		&syntheticInfo{fi: &vfs.FileInfo{Name: "b"}},
		&syntheticInfo{fi: &vfs.FileInfo{Name: "c"}},
	}
	l := listAt(src)
	dst := make([]fs.FileInfo, 2)
	n, err := l.ListAt(dst, 0)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, "a", dst[0].Name())
	require.Equal(t, "b", dst[1].Name())
}

func TestListAt_OffsetEOF(t *testing.T) {
	l := listAt([]fs.FileInfo{
		&syntheticInfo{fi: &vfs.FileInfo{Name: "a"}},
	})
	dst := make([]fs.FileInfo, 2)
	n, err := l.ListAt(dst, 5)
	require.Equal(t, 0, n)
	require.True(t, err == io.EOF)
}

func TestListAt_PartialThenEOF(t *testing.T) {
	l := listAt([]fs.FileInfo{
		&syntheticInfo{fi: &vfs.FileInfo{Name: "a"}},
		&syntheticInfo{fi: &vfs.FileInfo{Name: "b"}},
	})
	dst := make([]fs.FileInfo, 5)
	n, err := l.ListAt(dst, 0)
	require.True(t, err == io.EOF)
	require.Equal(t, 2, n)
}
