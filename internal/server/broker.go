package server

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

// realBroker implements WorkConnBroker using the Server's service map.
// It encapsulates the "request work conn → wait for arrival" flow
// that was previously duplicated in localRoute and handleRelayConn.
type realBroker struct {
	mu       *sync.RWMutex
	services map[string]*serviceEntry
}

func (b *realBroker) RequestAndWait(proxyAlias string, timeout time.Duration) (net.Conn, error) {
	b.mu.RLock()
	entry, ok := b.services[proxyAlias]
	b.mu.RUnlock()
	if !ok {
		return nil, ErrServiceNotFound
	}

	// Send ReqWorkConn on the client's control connection.
	if err := protocol.WriteEnvelope(entry.ctrlConn, &drppb.Envelope{
		Payload: &drppb.Envelope_ReqWorkConn{ReqWorkConn: &drppb.ReqWorkConn{
			ProxyAlias: proxyAlias,
		}},
	}); err != nil {
		return nil, fmt.Errorf("request work conn: %w", err)
	}

	// Wait for the work connection to arrive.
	select {
	case conn := <-entry.workQueue:
		return conn, nil
	case <-time.After(timeout):
		return nil, ErrWorkConnTimeout
	}
}
