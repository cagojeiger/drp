package server

import (
	"context"
	"fmt"
	"net"

	"github.com/hashicorp/memberlist"
	"google.golang.org/protobuf/proto"

	"github.com/cagojeiger/drp/internal/relay"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

// realRelayer implements RelayStreamer using mesh members and QUIC relay.
// It encapsulates peer address resolution and QUIC stream dialing.
type realRelayer struct {
	members func() []*memberlist.Node
	relay   *relay.RelayManager
}

func (r *realRelayer) DialStream(ctx context.Context, nodeID string) (net.Conn, error) {
	peerAddr := r.resolvePeerQuicAddr(nodeID)
	if peerAddr == "" {
		return nil, fmt.Errorf("peer %s: address not found", nodeID)
	}
	return r.relay.DialStream(ctx, peerAddr)
}

// resolvePeerQuicAddr looks up the QUIC address for a peer node.
// It first checks node metadata for an explicit QUIC address,
// then falls back to the node's advertised address with the local QUIC port.
func (r *realRelayer) resolvePeerQuicAddr(nodeID string) string {
	for _, member := range r.members() {
		if member.Name != nodeID {
			continue
		}
		meta := make([]byte, len(member.Meta))
		copy(meta, member.Meta)
		var ns drppb.NodeServices
		if err := proto.Unmarshal(meta, &ns); err != nil || ns.QuicAddr == "" {
			_, quicPort, _ := net.SplitHostPort(r.relay.Addr().String())
			return fmt.Sprintf("%s:%s", member.Addr, quicPort)
		}
		return ns.QuicAddr
	}
	return ""
}
