package mesh

import (
	"log"

	"github.com/hashicorp/memberlist"
)

func (d *DrpDelegate) NotifyJoin(node *memberlist.Node) {
	ns, ok := d.parseNodeMeta(node)
	if !ok {
		return
	}

	for _, hostname := range ns.Hostnames {
		if hostname == "" {
			continue
		}
		d.registry.Register(hostname, node.Name, "", false)
	}

	if d.mesh != nil {
		d.mesh.UpdateNodeMeta(node.Name, ns.Hostnames)
	}

	log.Printf("node joined: %s with %d services", node.Name, len(ns.Hostnames))
}

func (d *DrpDelegate) NotifyLeave(node *memberlist.Node) {
	d.registry.RemoveByNode(node.Name)
	if d.mesh != nil {
		d.mesh.RemoveNodeMeta(node.Name)
	}
	log.Printf("node left: %s", node.Name)
}

func (d *DrpDelegate) NotifyUpdate(node *memberlist.Node) {
	ns, ok := d.parseNodeMeta(node)
	if !ok {
		return
	}

	added, removed := syncRegistryFromHostnames(d.registry, node.Name, ns.Hostnames)

	if d.mesh != nil {
		d.mesh.UpdateNodeMeta(node.Name, ns.Hostnames)
	}

	log.Printf("node updated: %s, +%d -%d services", node.Name, added, removed)
}
