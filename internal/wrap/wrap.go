package wrap

import (
	"io"
	"net"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
)

// Wrap sends StartWorkConn and applies encryption/compression layers.
// Returns the wrapped connection ready for HTTP byte exchange.
//
// Wire order (both sides must match):
//   conn → [AES-128-CFB] → [snappy] → HTTP bytes
func Wrap(conn net.Conn, aesKey []byte, proxyName string, enc, comp bool) (io.ReadWriteCloser, error) {
	// 1. StartWorkConn (always plaintext)
	if err := msg.WriteMsg(conn, &msg.StartWorkConn{
		ProxyName: proxyName,
	}); err != nil {
		return nil, err
	}

	var r io.Reader = conn
	var w io.Writer = conn
	var closer io.Closer = conn

	// 2. Encryption layer
	if enc {
		encWriter, err := crypto.NewCryptoWriter(conn, aesKey)
		if err != nil {
			return nil, err
		}
		encReader, err := crypto.NewCryptoReader(conn, aesKey)
		if err != nil {
			return nil, err
		}
		r = encReader
		w = encWriter
	}

	// 3. Compression layer
	if comp {
		snappyWriter := crypto.NewSnappyWriter(w)
		snappyReader := crypto.NewSnappyReader(r)
		r = snappyReader
		w = snappyWriter
		closer = &compCloser{snappyWriter, conn}
	}

	return &wrappedConn{r: r, w: w, c: closer}, nil
}

type wrappedConn struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

func (wc *wrappedConn) Read(p []byte) (int, error)  { return wc.r.Read(p) }
func (wc *wrappedConn) Write(p []byte) (int, error)  { return wc.w.Write(p) }
func (wc *wrappedConn) Close() error                 { return wc.c.Close() }

// compCloser closes both snappy writer and underlying connection.
type compCloser struct {
	snappy io.WriteCloser
	conn   io.Closer
}

func (cc *compCloser) Close() error {
	cc.snappy.Close()
	return cc.conn.Close()
}
