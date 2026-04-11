package wrap

import (
	"io"
	"net"
	"testing"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
)

func TestWrapWithCachedKey(t *testing.T) {
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	key := crypto.DeriveKey("cached-token")
	go func() {
		wrapped, err := Wrap(drpsConn, key, "web", true, false)
		if err != nil {
			return
		}
		_, _ = wrapped.Write([]byte("hello cached key"))
	}()

	_, _ = msg.ReadMsg(frpcConn) // StartWorkConn
	reader, err := crypto.NewCryptoReader(frpcConn, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	if _, err := crypto.NewCryptoWriter(frpcConn, key); err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}
	got, err := io.ReadAll(io.LimitReader(reader, int64(len("hello cached key"))))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello cached key" {
		t.Fatalf("got=%q, want=%q", got, "hello cached key")
	}
}
