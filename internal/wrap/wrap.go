package wrap

import (
	"io"
	"net"
	"time"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
)

// Wrap sends StartWorkConn and applies encryption/compression layers.
// Returns the wrapped connection ready for HTTP byte exchange.
//
// Wire order (both sides must match):
//   conn → [AES-128-CFB] → [snappy] → HTTP bytes
func Wrap(conn net.Conn, aesKey []byte, proxyName string, enc, comp bool) (net.Conn, error) {
	// 1. StartWorkConn (always plaintext)
	if err := msg.WriteMsg(conn, &msg.StartWorkConn{
		ProxyName: proxyName,
	}); err != nil {
		return nil, err
	}

	// Fast path: no extra codec layer, use the raw connection directly.
	if !enc && !comp {
		return conn, nil
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

	return &wrappedConn{r: r, w: w, c: closer, conn: conn}, nil
}

type wrappedConn struct {
	r    io.Reader
	w    io.Writer
	c    io.Closer
	conn net.Conn // underlying connection for net.Conn interface
}

func (wc *wrappedConn) Read(p []byte) (int, error)         { return wc.r.Read(p) }
func (wc *wrappedConn) Write(p []byte) (int, error)         { return wc.w.Write(p) }
func (wc *wrappedConn) Close() error                        { return wc.c.Close() }
func (wc *wrappedConn) LocalAddr() net.Addr                 { return wc.conn.LocalAddr() }
func (wc *wrappedConn) RemoteAddr() net.Addr                { return wc.conn.RemoteAddr() }
func (wc *wrappedConn) SetDeadline(t time.Time) error       { return wc.conn.SetDeadline(t) }
func (wc *wrappedConn) SetReadDeadline(t time.Time) error   { return wc.conn.SetReadDeadline(t) }
func (wc *wrappedConn) SetWriteDeadline(t time.Time) error  { return wc.conn.SetWriteDeadline(t) }

// compCloser closes both snappy writer and underlying connection.
type compCloser struct {
	snappy io.WriteCloser
	conn   io.Closer
}

func (cc *compCloser) Close() error {
	cc.snappy.Close()
	return cc.conn.Close()
}
