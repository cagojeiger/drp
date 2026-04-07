package wrap

import (
	"io"
	"net"
	"testing"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
)

func TestWrapStartWorkConn(t *testing.T) {
	// drps 쪽 → Wrap → frpc 쪽에서 StartWorkConn 수신 확인
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	go func() {
		_, err := Wrap(drpsConn, "test-token", "web", false, false)
		if err != nil {
			t.Errorf("Wrap: %v", err)
		}
	}()

	// frpc 쪽: StartWorkConn 메시지 수신
	m, err := msg.ReadMsg(frpcConn)
	if err != nil {
		t.Fatalf("ReadMsg: %v", err)
	}
	swc, ok := m.(*msg.StartWorkConn)
	if !ok {
		t.Fatalf("expected *StartWorkConn, got %T", m)
	}
	if swc.ProxyName != "web" {
		t.Errorf("ProxyName = %q, want %q", swc.ProxyName, "web")
	}
}

func TestWrapNoEncNoComp(t *testing.T) {
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	var wrapped io.ReadWriteCloser
	go func() {
		var err error
		wrapped, err = Wrap(drpsConn, "test-token", "web", false, false)
		if err != nil {
			return
		}
		wrapped.Write([]byte("hello plain"))
	}()

	// frpc: StartWorkConn 읽기
	msg.ReadMsg(frpcConn)

	// frpc: 평문 그대로 수신
	buf := make([]byte, 64)
	n, err := frpcConn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello plain" {
		t.Errorf("got %q, want %q", buf[:n], "hello plain")
	}
}

func TestWrapEncryption(t *testing.T) {
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapped, err := Wrap(drpsConn, "test-token", "web", true, false)
		if err != nil {
			return
		}
		wrapped.Write([]byte("hello encrypted"))
	}()

	// frpc: StartWorkConn (평문)
	msg.ReadMsg(frpcConn)

	// frpc: AES 양방향 (frpc는 Reader 먼저 → drps Writer의 IV 수신, Writer → drps Reader에 IV 전송)
	key := crypto.DeriveKey("test-token")
	reader, err := crypto.NewCryptoReader(frpcConn, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	// drps의 Reader가 frpc Writer의 IV를 기다리므로, frpc도 Writer 생성 필요
	_, err = crypto.NewCryptoWriter(frpcConn, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}

	buf := make([]byte, 64)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello encrypted" {
		t.Errorf("got %q, want %q", buf[:n], "hello encrypted")
	}
}

func TestWrapCompression(t *testing.T) {
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapped, err := Wrap(drpsConn, "test-token", "web", false, true)
		if err != nil {
			return
		}
		w := wrapped.(io.WriteCloser)
		w.Write([]byte("hello compressed"))
		w.Close()
	}()

	// frpc: StartWorkConn (평문)
	msg.ReadMsg(frpcConn)

	// frpc: snappy 해제
	reader := crypto.NewSnappyReader(frpcConn)
	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(buf) != "hello compressed" {
		t.Errorf("got %q, want %q", buf, "hello compressed")
	}
}

func TestWrapEncAndComp(t *testing.T) {
	drpsConn, frpcConn := net.Pipe()
	defer drpsConn.Close()
	defer frpcConn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrapped, err := Wrap(drpsConn, "test-token", "web", true, true)
		if err != nil {
			return
		}
		w := wrapped.(io.WriteCloser)
		w.Write([]byte("hello both"))
		w.Close()
	}()

	// frpc: StartWorkConn (평문)
	msg.ReadMsg(frpcConn)

	// frpc: AES 양방향 설정
	key := crypto.DeriveKey("test-token")
	aesReader, err := crypto.NewCryptoReader(frpcConn, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	_, err = crypto.NewCryptoWriter(frpcConn, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}

	// snappy 해제
	snappyReader := crypto.NewSnappyReader(aesReader)

	buf, err := io.ReadAll(snappyReader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(buf) != "hello both" {
		t.Errorf("got %q, want %q", buf, "hello both")
	}
}
