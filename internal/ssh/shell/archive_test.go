package shell

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/vfs"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestZipUnzipRoundTrip(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFile(t, filepath.Join(dir, "Documents", "sub", "deep.txt"), "deep\n")

	out, stderr, st := runStatus(t, sh, "zip -r out Documents hello.txt")
	if st != 0 || stderr != "" {
		t.Fatalf("zip: status %d: %s", st, stderr)
	}
	for _, want := range []string{"adding: Documents/ (stored 0%)", "adding: Documents/sub/deep.txt (deflated", "adding: hello.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}

	out, stderr, st = runStatus(t, sh, "unzip -d x out.zip")
	if st != 0 || stderr != "" {
		t.Fatalf("unzip: status %d: %s", st, stderr)
	}
	if !strings.Contains(out, "inflating: Documents/notes.txt") {
		t.Fatalf("expected inflating lines, got %q", out)
	}
	for name, want := range map[string]string{
		"x/Documents/notes.txt":    "line1\nline2\nline3\n",
		"x/Documents/sub/deep.txt": "deep\n",
		"x/hello.txt":              "hello world\n",
	} {
		if got := readFile(t, filepath.Join(dir, name)); got != want {
			t.Fatalf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestZipWithoutRecurseAddsOnlyTheDirectory(t *testing.T) {
	sh, dir := newTestShell(t)
	runStatus(t, sh, "zip -q out.zip Documents")
	zr, err := zip.OpenReader(filepath.Join(dir, "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "Documents/" {
		t.Fatalf("expected only Documents/, got %d entries", len(zr.File))
	}
}

func TestZipStoresCompressedFormats(t *testing.T) {
	sh, dir := newTestShell(t)
	writeFile(t, filepath.Join(dir, "photo.jpg"), strings.Repeat("x", 4096))
	runStatus(t, sh, "zip -q out.zip photo.jpg hello.txt")
	zr, err := zip.OpenReader(filepath.Join(dir, "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	methods := map[string]uint16{}
	for _, f := range zr.File {
		methods[f.Name] = f.Method
	}
	if methods["photo.jpg"] != zip.Store || methods["hello.txt"] != zip.Deflate {
		t.Fatalf("expected photo.jpg stored and hello.txt deflated, got %v", methods)
	}
}

func TestZipLeavesItselfOut(t *testing.T) {
	sh, dir := newTestShell(t)
	runStatus(t, sh, "zip -q out.zip hello.txt")
	runStatus(t, sh, "zip -rq out.zip .")
	zr, err := zip.OpenReader(filepath.Join(dir, "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == "out.zip" {
			t.Fatal("the archive contains itself")
		}
	}
}

func TestUnzipList(t *testing.T) {
	sh, _ := newTestShell(t)
	runStatus(t, sh, "zip -rq out.zip Documents hello.txt")
	out, _, st := runStatus(t, sh, "unzip -l out.zip")
	if st != 0 {
		t.Fatalf("status %d", st)
	}
	for _, want := range []string{"Documents/notes.txt", "hello.txt", "3 files"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestUnzipOverwrite(t *testing.T) {
	sh, dir := newTestShell(t)
	runStatus(t, sh, "zip -q out.zip hello.txt")
	writeFile(t, filepath.Join(dir, "hello.txt"), "changed\n")

	out, _, st := runStatus(t, sh, "unzip out.zip")
	if st != 0 || !strings.Contains(out, "skipping: hello.txt (exists; use -o to overwrite)") {
		t.Fatalf("expected the existing file skipped, status %d: %q", st, out)
	}
	if got := readFile(t, filepath.Join(dir, "hello.txt")); got != "changed\n" {
		t.Fatalf("existing file overwritten without -o: %q", got)
	}
	runStatus(t, sh, "unzip -oq out.zip")
	if got := readFile(t, filepath.Join(dir, "hello.txt")); got != "hello world\n" {
		t.Fatalf("expected -o to overwrite, got %q", got)
	}
}

func TestUnzipMembers(t *testing.T) {
	sh, dir := newTestShell(t)
	runStatus(t, sh, "zip -rq out.zip Documents hello.txt")
	_, stderr, st := runStatus(t, sh, "unzip -q -d x out.zip Documents nosuch")
	if st == 0 || !strings.Contains(stderr, "nosuch: not found in archive") {
		t.Fatalf("expected a missing member reported, status %d: %q", st, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "x", "Documents", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x", "hello.txt")); !os.IsNotExist(err) {
		t.Fatal("expected hello.txt left in the archive")
	}
}

func TestUnzipRefusesEscapingPaths(t *testing.T) {
	sh, dir := newTestShell(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"../evil.txt", "a/../../evil2.txt", "/abs.txt", "ok.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, "x"); err != nil {
			t.Fatal(err)
		}
	}
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "/etc/passwd"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "Documents", "evil.zip"), buf.String())

	_, stderr, st := runStatus(t, sh, "cd Documents && unzip -q -d out evil.zip")
	if st == 0 {
		t.Fatal("expected a non-zero status")
	}
	for _, want := range []string{"../evil.txt", "a/../../evil2.txt", "link: not a regular file"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("expected %q reported in %q", want, stderr)
		}
	}
	for _, name := range []string{"Documents/evil.txt", "evil.txt", "evil2.txt", "Documents/out/link"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written", name)
		}
	}
	for _, name := range []string{"Documents/out/ok.txt", "Documents/out/abs.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTarRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, create, extract string }{
		{"bundled", "tar czf out.tgz Documents hello.txt", "tar xf out.tgz -C x"},
		{"dashed", "tar -czf out.tgz Documents hello.txt", "tar -xzf out.tgz -C x"},
		{"long", "tar --create --gzip --file=out.tgz Documents hello.txt", "tar --extract --file out.tgz --directory=x"},
		{"plain", "tar -cf out.tar Documents hello.txt", "tar -x -f out.tar -C x"},
		{"pipeline", "true", "tar -cf - Documents hello.txt | tar -xC x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, dir := newTestShell(t)
			writeFile(t, filepath.Join(dir, "Documents", "sub", "deep.txt"), "deep\n")
			if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, stderr, st := runStatus(t, sh, tc.create); st != 0 {
				t.Fatalf("create: status %d: %s", st, stderr)
			}
			if _, stderr, st := runStatus(t, sh, tc.extract); st != 0 || stderr != "" {
				t.Fatalf("extract: status %d: %s", st, stderr)
			}
			for name, want := range map[string]string{
				"x/Documents/notes.txt":    "line1\nline2\nline3\n",
				"x/Documents/sub/deep.txt": "deep\n",
				"x/hello.txt":              "hello world\n",
			} {
				if got := readFile(t, filepath.Join(dir, name)); got != want {
					t.Fatalf("%s: got %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestTarList(t *testing.T) {
	sh, _ := newTestShell(t)
	runStatus(t, sh, "tar czf out.tgz Documents hello.txt")
	out, _, st := runStatus(t, sh, "tar tf out.tgz")
	if st != 0 || out != "Documents/\nDocuments/notes.txt\nhello.txt\n" {
		t.Fatalf("status %d: %q", st, out)
	}
	out, _, _ = runStatus(t, sh, "tar tvf out.tgz hello.txt")
	if !strings.Contains(out, "-rw-r--r-- testuser/testuser        12 ") || !strings.HasSuffix(out, " hello.txt\n") {
		t.Fatalf("unexpected verbose listing %q", out)
	}
}

func TestTarCreateVerboseAndRelativeToC(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _, st := runStatus(t, sh, "tar cvf out.tar -C Documents notes.txt")
	if st != 0 || out != "notes.txt\n" {
		t.Fatalf("status %d: %q", st, out)
	}
	out, _, _ = runStatus(t, sh, "tar tf out.tar")
	if out != "notes.txt\n" {
		t.Fatalf("expected names relative to -C, got %q", out)
	}
}

func TestTarRefusesEscapingAndSpecialEntries(t *testing.T) {
	sh, dir := newTestShell(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	add(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "x")
	add(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	add(&tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}, "")
	add(&tar.Header{Name: "/abs.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "abs")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "Documents", "evil.tar"), buf.String())

	_, stderr, st := runStatus(t, sh, "cd Documents && tar xf evil.tar")
	if st == 0 {
		t.Fatal("expected a non-zero status")
	}
	for _, want := range []string{"../evil.txt: skipping", "link: skipping", "hard: skipping", "Removing leading '/'"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("expected %q in %q", want, stderr)
		}
	}
	for _, name := range []string{"evil.txt", "Documents/link", "Documents/hard"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written", name)
		}
	}
	if got := readFile(t, filepath.Join(dir, "Documents", "abs.txt")); got != "abs" {
		t.Fatalf("abs.txt: %q", got)
	}
}

func TestWindowReaderAt(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	want := bytes.NewReader(data)
	w := newWindowReaderAt(want, 64)
	for _, tc := range []struct {
		off int64
		n   int
	}{{0, 10}, {5, 10}, {60, 10}, {990, 20}, {1000, 1}, {1200, 4}, {100, 64}, {100, 200}, {3, 3}} {
		got, wantBuf := make([]byte, tc.n), make([]byte, tc.n)
		gn, gerr := w.ReadAt(got, tc.off)
		wn, werr := want.ReadAt(wantBuf, tc.off)
		if gn != wn || !errors.Is(gerr, werr) && gerr != werr || !bytes.Equal(got[:gn], wantBuf[:wn]) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v; want %d, %v", tc.n, tc.off, gn, gerr, wn, werr)
		}
	}
}

func TestParseTarArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want tarOpts
	}{
		{[]string{"czvf", "a.tgz", "d"}, tarOpts{mode: 'c', gzip: true, verbose: true, file: "a.tgz", operands: []string{"d"}}},
		{[]string{"xfC", "a.tar", "out"}, tarOpts{mode: 'x', file: "a.tar", dir: "out"}},
		{[]string{"-xzfa.tgz", "d"}, tarOpts{mode: 'x', gzip: true, file: "a.tgz", operands: []string{"d"}}},
		{[]string{"-t", "--file", "a.tar", "--", "-odd"}, tarOpts{mode: 't', file: "a.tar", operands: []string{"-odd"}}},
		{[]string{"-c", "d", "-v"}, tarOpts{mode: 'c', file: "-", verbose: true, operands: []string{"d"}}},
	} {
		got, err := parseTarArgs(tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !reflect.DeepEqual(*got, tc.want) {
			t.Fatalf("%v: got %+v, want %+v", tc.args, *got, tc.want)
		}
	}
	for _, args := range [][]string{{"-cx", "d"}, {"-f"}, {"d"}, {"-q"}, {"--frobnicate"}} {
		if _, err := parseTarArgs(args); err == nil {
			t.Fatalf("%v: expected an error", args)
		}
	}
}

// failingPutFS fails every Put after reading part of it, as a write over
// quota does.
type failingPutFS struct{ *testFileFS }

var errOverQuota = errors.New("quota exceeded")

func (f failingPutFS) Put(p string, content io.Reader) error {
	if _, err := io.CopyN(io.Discard, content, 1); err != nil && err != io.EOF {
		return err
	}
	return errOverQuota
}

func TestArchiveWriteFailuresAreReported(t *testing.T) {
	sh, dir := newTestShell(t)
	runStatus(t, sh, "zip -rq out.zip Documents")
	runStatus(t, sh, "tar cf out.tar Documents")

	ofs := vfs.NewOverlayFS(nil)
	ofs.Mount("/files", failingPutFS{&testFileFS{root: dir}})
	failing := New("testuser", ofs, nil, nil, nil, slog.New(slog.DiscardHandler))
	for _, cmd := range []string{"zip -rq new.zip Documents", "tar cf new.tar Documents", "unzip -oq -d x out.zip", "tar xf out.tar -C Documents"} {
		_, stderr, st := runStatus(t, failing, "cd /files && "+cmd)
		if st == 0 || !strings.Contains(stderr, "quota exceeded") {
			t.Fatalf("%s: expected the failed write reported, status %d: %q", cmd, st, stderr)
		}
	}
}
