package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

const remoteHead = 256 << 10

// errRefused is wrapped when the origin answers 403 or 410: its URL has
// expired or is bound elsewhere, and a fresh one may work.
var errRefused = errors.New("feed: remote refused")

// remoteFile is an io.ReaderAt over an HTTP resource. It fetches
// bytes=0-(remoteHead-1) once, learns the size from Content-Range, and
// answers reads past the head with further Range requests.
type remoteFile struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64
	head   []byte
}

func openRemote(ctx context.Context, client *http.Client, url string) (*remoteFile, error) {
	f := &remoteFile{ctx: ctx, client: client, url: url}
	head, total, err := f.fetch(0, remoteHead-1)
	if err != nil {
		return nil, err
	}
	f.head, f.size = head, total
	return f, nil
}

// fetch reads bytes first..last and returns them with the resource's size.
func (f *remoteFile) fetch(first, last int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("feed: remote request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", first, last))
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("feed: remote range %d-%d: %w", first, last, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusForbidden, http.StatusGone:
		return nil, 0, fmt.Errorf("%w: %s", errRefused, resp.Status)
	default:
		// A 200 would stream the whole body, so it is refused too.
		return nil, 0, fmt.Errorf("feed: remote range %d-%d: unexpected %s", first, last, resp.Status)
	}
	total, err := contentRangeSize(resp.Header.Get("Content-Range"))
	if err != nil {
		return nil, 0, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, last-first+1))
	if err != nil {
		return nil, 0, fmt.Errorf("feed: remote read %d-%d: %w", first, last, err)
	}
	return body, total, nil
}

// contentRangeSize parses the total from "bytes 0-99/1234".
func contentRangeSize(h string) (int64, error) {
	_, total, ok := strings.Cut(h, "/")
	if !ok {
		return 0, fmt.Errorf("feed: remote Content-Range %q has no size", h)
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("feed: remote Content-Range %q: %w", h, err)
	}
	return n, nil
}

// Size is the resource's length in bytes.
func (f *remoteFile) Size() int64 { return f.size }

func (f *remoteFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("feed: remote read at negative offset")
	}
	if off >= f.size {
		return 0, io.EOF
	}
	want := int64(len(p))
	eof := false
	if off+want > f.size {
		want = f.size - off
		eof = true
	}
	if want == 0 {
		return 0, nil
	}
	var n int
	if off+want <= int64(len(f.head)) {
		n = copy(p[:want], f.head[off:])
	} else {
		body, _, err := f.fetch(off, off+want-1)
		if err != nil {
			return 0, err
		}
		n = copy(p[:want], body)
		if int64(n) < want {
			return n, io.ErrUnexpectedEOF
		}
	}
	if eof {
		return n, io.EOF
	}
	return n, nil
}

// RemoteTrack reads the index of the track at url.
func RemoteTrack(ctx context.Context, client *http.Client, url string) (hls.Track, error) {
	f, err := openRemote(ctx, client, url)
	if err != nil {
		return hls.Track{}, err
	}
	t, err := hls.ReadTrack(f, f.Size())
	if err != nil {
		return hls.Track{}, fmt.Errorf("feed: read remote track: %w", err)
	}
	return t, nil
}
