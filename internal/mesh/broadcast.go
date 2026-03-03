package mesh

import (
	"log"

	"github.com/hashicorp/memberlist"
	"google.golang.org/protobuf/proto"

	drppb "github.com/cagojeiger/drp/proto/drp"
)

type serviceBroadcast struct {
	name string
	msg  []byte
}

func (b *serviceBroadcast) Invalidates(other memberlist.Broadcast) bool {
	ob, ok := other.(*serviceBroadcast)
	if !ok {
		return false
	}
	return b.name == ob.name
}

func (b *serviceBroadcast) Message() []byte { return b.msg }

func (b *serviceBroadcast) Finished() {}

func (b *serviceBroadcast) Name() string { return b.name }

func (d *DrpDelegate) BroadcastServiceUpdate(su *drppb.ServiceUpdate) {
	if su == nil {
		return
	}

	msg, err := proto.Marshal(su)
	if err != nil {
		log.Printf("mesh: failed to marshal service update: %v", err)
		return
	}

	d.mu.RLock()
	q := d.broadcasts
	d.mu.RUnlock()
	if q == nil {
		return
	}

	q.QueueBroadcast(&serviceBroadcast{
		name: "svc:" + su.Hostname,
		msg:  msg,
	})
}

func (d *DrpDelegate) handleBroadcastMessage(buf []byte) {
	var su drppb.ServiceUpdate
	if err := proto.Unmarshal(buf, &su); err != nil {
		log.Printf("mesh: failed to unmarshal service update: %v", err)
		return
	}

	switch su.Action {
	case ActionAdd:
		if su.Hostname == "" || su.NodeId == "" {
			return
		}
		d.registry.Register(su.Hostname, su.NodeId, su.ProxyAlias, false)
	case ActionRemove:
		if su.Hostname == "" {
			return
		}
		d.registry.Unregister(su.Hostname)
	default:
		log.Printf("mesh: unknown service update action %q", su.Action)
		return
	}

	d.mu.RLock()
	q := d.broadcasts
	d.mu.RUnlock()
	if q == nil {
		return
	}

	cp := make([]byte, len(buf))
	copy(cp, buf)
	q.QueueBroadcast(&serviceBroadcast{
		name: "svc:" + su.Hostname,
		msg:  cp,
	})
}
