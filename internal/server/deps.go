package server

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/cagojeiger/drp/internal/registry"
)

// Sentinel errors returned by interface implementations.
var (
	ErrServiceNotFound = errors.New("service not found")
	ErrWorkConnTimeout = errors.New("work connection timeout")
)

// ServiceLookup resolves a hostname to service routing info.
type ServiceLookup interface {
	Lookup(hostname string) (registry.ServiceInfo, bool)
}

// WorkConnBroker requests a work connection from a drpc client
// and waits for it to arrive within the given timeout.
type WorkConnBroker interface {
	RequestAndWait(proxyAlias string, timeout time.Duration) (net.Conn, error)
}

// RelayStreamer opens a QUIC relay stream to a peer node by its node ID.
// It encapsulates peer address resolution and QUIC connection management.
type RelayStreamer interface {
	DialStream(ctx context.Context, nodeID string) (net.Conn, error)
}

// ServiceRegistrar manages service lifecycle in the mesh.
type ServiceRegistrar interface {
	RegisterService(alias, hostname string)
	UnregisterService(hostname string)
}
