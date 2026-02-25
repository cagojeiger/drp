package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// quicStreamConn wraps a quic.Stream as a net.Conn.
// It delegates LocalAddr/RemoteAddr to the parent QUIC connection.
type quicStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func QuicStreamToNetConn(s *quic.Stream, c *quic.Conn) net.Conn {
	return &quicStreamConn{Stream: s, conn: c}
}

func (c *quicStreamConn) LocalAddr() net.Addr {
	if c.conn != nil {
		return c.conn.LocalAddr()
	}
	return &net.UDPAddr{}
}

func (c *quicStreamConn) RemoteAddr() net.Addr {
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return &net.UDPAddr{}
}

func (c *quicStreamConn) Close() error {
	c.CancelRead(0)
	return c.Stream.Close()
}

func GenerateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate ECDSA key: %w", err)
	}

	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate certificate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             now,
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create self-signed certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal ECDSA private key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load X509 key pair: %w", err)
	}
	return cert, nil
}

func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"drp"},
		MinVersion:   tls.VersionTLS13,
	}
}

func ClientTLSConfig(insecure bool) *tls.Config {
	return &tls.Config{
		NextProtos:         []string{"drp"},
		InsecureSkipVerify: insecure,
		MinVersion:         tls.VersionTLS13,
	}
}

type RelayManager struct {
	tlsCert  tls.Certificate
	listener *quic.Listener

	mu    sync.RWMutex
	peers map[string]*quic.Conn

	streams   chan net.Conn
	acceptErr chan error
	cancel    context.CancelFunc
}

func NewRelayManager(tlsCert tls.Certificate) *RelayManager {
	return &RelayManager{
		tlsCert:   tlsCert,
		peers:     make(map[string]*quic.Conn),
		streams:   make(chan net.Conn, 256),
		acceptErr: make(chan error, 1),
	}
}

func (rm *RelayManager) Listen(addr string) error {
	tlsCfg := ServerTLSConfig(rm.tlsCert)
	quicCfg := &quic.Config{
		MaxIdleTimeout:     30 * time.Second,
		MaxIncomingStreams: 10000,
		KeepAlivePeriod:    10 * time.Second,
	}

	listener, err := quic.ListenAddr(addr, tlsCfg, quicCfg)
	if err != nil {
		return fmt.Errorf("listen QUIC on %s: %w", addr, err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	rm.mu.Lock()
	rm.listener = listener
	rm.cancel = cancel
	rm.mu.Unlock()

	go rm.acceptLoop(runCtx)
	return nil
}

func (rm *RelayManager) acceptLoop(ctx context.Context) {
	for {
		rm.mu.RLock()
		listener := rm.listener
		rm.mu.RUnlock()
		if listener == nil {
			return
		}

		conn, err := listener.Accept(ctx)
		if err != nil {
			select {
			case rm.acceptErr <- err:
			default:
			}
			return
		}
		go rm.handleConn(ctx, conn)
	}
}

func (rm *RelayManager) handleConn(ctx context.Context, conn *quic.Conn) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}

		wrapped := QuicStreamToNetConn(stream, conn)
		select {
		case rm.streams <- wrapped:
		case <-ctx.Done():
			_ = wrapped.Close()
			return
		}
	}
}

func (rm *RelayManager) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case conn := <-rm.streams:
		return conn, nil
	case err := <-rm.acceptErr:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (rm *RelayManager) Addr() net.Addr {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	if rm.listener == nil {
		return nil
	}
	return rm.listener.Addr()
}

func (rm *RelayManager) DialStream(ctx context.Context, peerAddr string) (net.Conn, error) {
	rm.mu.RLock()
	conn := rm.peers[peerAddr]
	rm.mu.RUnlock()

	if conn != nil {
		stream, err := conn.OpenStreamSync(ctx)
		if err == nil {
			return QuicStreamToNetConn(stream, conn), nil
		}

		rm.mu.Lock()
		if rm.peers[peerAddr] == conn {
			delete(rm.peers, peerAddr)
		}
		rm.mu.Unlock()
	}

	tlsCfg := ClientTLSConfig(true)
	quicCfg := &quic.Config{
		MaxIdleTimeout:     30 * time.Second,
		MaxIncomingStreams: 10000,
		KeepAlivePeriod:    10 * time.Second,
	}

	newConn, err := quic.DialAddr(ctx, peerAddr, tlsCfg, quicCfg)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC peer %s: %w", peerAddr, err)
	}

	rm.mu.Lock()
	rm.peers[peerAddr] = newConn
	rm.mu.Unlock()

	stream, err := newConn.OpenStreamSync(ctx)
	if err != nil {
		rm.mu.Lock()
		if rm.peers[peerAddr] == newConn {
			delete(rm.peers, peerAddr)
		}
		rm.mu.Unlock()
		_ = newConn.CloseWithError(0, "")
		return nil, fmt.Errorf("open QUIC stream to %s: %w", peerAddr, err)
	}

	return QuicStreamToNetConn(stream, newConn), nil
}

func (rm *RelayManager) Close() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.cancel != nil {
		rm.cancel()
		rm.cancel = nil
	}

	if rm.listener != nil {
		_ = rm.listener.Close()
		rm.listener = nil
	}

	for addr, conn := range rm.peers {
		_ = conn.CloseWithError(0, "")
		delete(rm.peers, addr)
	}
	select {
	case rm.acceptErr <- net.ErrClosed:
	default:
	}

	return nil
}
