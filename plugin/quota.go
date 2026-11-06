package plugin

import (
	"fmt"
	"io"
)

// LimitQuota returns a reader of r that fails with ErrQuotaExceeded once
// more than allowance bytes have been read: the room QuotaRemaining
// reported, plus the size of whatever the write replaces. A negative
// allowance means no limit. A writer that writes to a temporary file and
// renames it into place, as vfs.FS.Put and Storage.Put do, then fails
// before the rename, leaving the old file as it was.
func LimitQuota(r io.Reader, allowance int64) io.Reader {
	if allowance < 0 {
		return r
	}
	return &quotaReader{r: r, left: allowance}
}

type quotaReader struct {
	r    io.Reader
	left int64
}

func (q *quotaReader) Read(p []byte) (int, error) {
	// Read one byte past the allowance, so a body of exactly the allowance
	// passes and only a larger one fails.
	if int64(len(p)) > q.left+1 {
		p = p[:q.left+1]
	}
	n, err := q.r.Read(p)
	q.left -= int64(n)
	if q.left < 0 {
		return n, fmt.Errorf("%w: the upload is larger than the space left", ErrQuotaExceeded)
	}
	return n, err
}
