package crypto

import (
	"io"

	"github.com/golang/snappy"
)

// NewSnappyWriter returns a writer that compresses data with snappy.
// Each Write is flushed immediately to avoid deadlocks in bidirectional streams.
func NewSnappyWriter(w io.Writer) io.WriteCloser {
	return &autoFlushSnappyWriter{w: snappy.NewBufferedWriter(w)}
}

type autoFlushSnappyWriter struct {
	w *snappy.Writer
}

func (s *autoFlushSnappyWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		return n, err
	}
	if err := s.w.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

func (s *autoFlushSnappyWriter) Close() error {
	return s.w.Close()
}

// NewSnappyReader returns a reader that decompresses snappy data.
func NewSnappyReader(r io.Reader) io.Reader {
	return snappy.NewReader(r)
}
