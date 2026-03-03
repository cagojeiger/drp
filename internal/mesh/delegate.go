package mesh

import (
	"log"
	"sync"

	"github.com/hashicorp/memberlist"
	"google.golang.org/protobuf/proto"

	"github.com/cagojeiger/drp/internal/registry"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

var (
	_ memberlist.Delegate      = (*DrpDelegate)(nil)
	_ memberlist.EventDelegate = (*DrpDelegate)(nil)
)

type DrpDelegate struct {
	nodeID     string
	quicAddr   string
	registry   *registry.Registry
	broadcasts *memberlist.TransmitLimitedQueue
	mesh       *Mesh
	mu         sync.RWMutex
}

func NewDrpDelegate(nodeID string, reg *registry.Registry) *DrpDelegate {
	return &DrpDelegate{
		nodeID:   nodeID,
		registry: reg,
	}
}

func (d *DrpDelegate) SetQuicAddr(addr string) {
	d.mu.Lock()
	d.quicAddr = addr
	d.mu.Unlock()
}

func (d *DrpDelegate) NodeMeta(limit int) []byte {
	hostnames := buildHostnames(d.registry)

	d.mu.RLock()
	qa := d.quicAddr
	d.mu.RUnlock()

	meta, err := proto.Marshal(&drppb.NodeServices{Hostnames: hostnames, QuicAddr: qa})
	if err != nil {
		log.Printf("mesh: failed to marshal node meta: %v", err)
		return nil
	}

	if len(meta) > limit {
		log.Printf("mesh: node meta exceeds limit (%d > %d)", len(meta), limit)
		return nil
	}

	return meta
}

func (d *DrpDelegate) NotifyMsg(buf []byte) {
	cp := make([]byte, len(buf))
	copy(cp, buf)
	go d.handleBroadcastMessage(cp)
}

func (d *DrpDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	d.mu.RLock()
	q := d.broadcasts
	d.mu.RUnlock()
	if q == nil {
		return nil
	}
	return q.GetBroadcasts(overhead, limit)
}

func (d *DrpDelegate) LocalState(join bool) []byte {
	hostnames := buildHostnames(d.registry)

	d.mu.RLock()
	qa := d.quicAddr
	d.mu.RUnlock()

	payload, err := proto.Marshal(&drppb.NodeServices{Hostnames: hostnames, QuicAddr: qa})
	if err != nil {
		log.Printf("mesh: failed to marshal local state: %v", err)
		return nil
	}

	if len(d.nodeID) > 255 {
		log.Printf("mesh: node id too long for local state encoding: %d", len(d.nodeID))
		return nil
	}

	out := make([]byte, 1+len(d.nodeID)+len(payload))
	out[0] = byte(len(d.nodeID))
	copy(out[1:], d.nodeID)
	copy(out[1+len(d.nodeID):], payload)
	return out
}

func (d *DrpDelegate) MergeRemoteState(buf []byte, join bool) {
	if len(buf) < 1 {
		return
	}

	nodeIDLen := int(buf[0])
	if len(buf) < 1+nodeIDLen {
		log.Printf("mesh: invalid remote state payload length")
		return
	}

	nodeID := string(buf[1 : 1+nodeIDLen])
	if nodeID == "" {
		log.Printf("mesh: remote state missing node id")
		return
	}

	metaBytes := buf[1+nodeIDLen:]
	var ns drppb.NodeServices
	if err := proto.Unmarshal(metaBytes, &ns); err != nil {
		log.Printf("mesh: failed to unmarshal remote state for %s: %v", nodeID, err)
		return
	}

	syncRegistryFromHostnames(d.registry, nodeID, ns.Hostnames)
}

func (d *DrpDelegate) SetBroadcastQueue(q *memberlist.TransmitLimitedQueue) {
	d.mu.Lock()
	d.broadcasts = q
	d.mu.Unlock()
}

func (d *DrpDelegate) parseNodeMeta(node *memberlist.Node) (*drppb.NodeServices, bool) {
	meta := make([]byte, len(node.Meta))
	copy(meta, node.Meta)
	var ns drppb.NodeServices
	if err := proto.Unmarshal(meta, &ns); err != nil {
		log.Printf("mesh: failed to unmarshal node meta for %s: %v", node.Name, err)
		return nil, false
	}
	return &ns, true
}
