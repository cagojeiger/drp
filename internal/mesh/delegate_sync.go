package mesh

import (
	"github.com/cagojeiger/drp/internal/registry"
)

// buildHostnames collects hostnames from local services.
// Used by NodeMeta() and LocalState() to avoid duplication.
func buildHostnames(reg *registry.Registry) []string {
	services := reg.LocalServices()
	hostnames := make([]string, 0, len(services))
	for _, svc := range services {
		hostnames = append(hostnames, svc.Hostname)
	}
	return hostnames
}

// syncRegistryFromHostnames synchronizes the registry with an incoming hostname set.
// It registers new hostnames and unregisters removed ones.
// Returns (added, removed) counts.
// Used by MergeRemoteState() and NotifyUpdate() to avoid duplication.
func syncRegistryFromHostnames(reg *registry.Registry, nodeID string, incoming []string) (added, removed int) {
	existing := reg.ListByNode(nodeID)
	existingSet := make(map[string]struct{}, len(existing))
	for _, svc := range existing {
		existingSet[svc.Hostname] = struct{}{}
	}

	incomingSet := make(map[string]struct{}, len(incoming))
	for _, hostname := range incoming {
		if hostname == "" {
			continue
		}
		incomingSet[hostname] = struct{}{}
		if _, ok := existingSet[hostname]; !ok {
			reg.Register(hostname, nodeID, "", false)
			added++
		}
	}

	for _, svc := range existing {
		if _, ok := incomingSet[svc.Hostname]; !ok {
			reg.Unregister(svc.Hostname)
			removed++
		}
	}

	return added, removed
}
