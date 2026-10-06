package provider

import (
	"errors"
	"io"
)

const maxResponseStreamBytes = 32 << 20

var errResponseStreamTooLarge = errors.New("upstream stream exceeds 32 MiB")

// responseStreamReader bounds all wire bytes, including comments and unfinished
// frames. One extra byte distinguishes an exact-size body from an oversized one.
type responseStreamReader struct {
	source    io.Reader
	remaining int
}

func (r *responseStreamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining < 0 {
		return 0, errResponseStreamTooLarge
	}
	n, err := r.source.Read(p[:min(len(p), r.remaining+1)])
	if n > r.remaining {
		r.remaining = -1
		return 0, errResponseStreamTooLarge
	}
	r.remaining -= n
	return n, err
}
