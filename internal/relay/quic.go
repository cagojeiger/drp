package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	quicIdleTimeout     = 30 * time.Second
	quicMaxStreams      = 10000
	quicKeepAlive       = 10 * time.Second
	streamChannelBuffer = 256
)

func defaultQuicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:     quicIdleTimeout,
		MaxIncomingStreams: quicMaxStreams,
		KeepAlivePeriod:    quicKeepAlive,
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
		streams:   make(chan net.Conn, streamChannelBuffer),
		acceptErr: make(chan error, 1),
	}
}

func (rm *RelayManager) Listen(addr string) error {
	tlsCfg := ServerTLSConfig(rm.tlsCert)

	listener, err := quic.ListenAddr(addr, tlsCfg, defaultQuicConfig())
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
		go rm.acceptQuicStreams(ctx, conn)
	}
}

func (rm *RelayManager) acceptQuicStreams(ctx context.Context, conn *quic.Conn) {
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

	newConn, err := quic.DialAddr(ctx, peerAddr, tlsCfg, defaultQuicConfig())
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
