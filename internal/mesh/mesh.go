package mesh

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"

	"github.com/cagojeiger/drp/internal/registry"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

type MeshConfig struct {
	NodeID   string
	BindAddr string
	BindPort int
	QuicAddr string
}

type Mesh struct {
	config   MeshConfig
	list     *memberlist.Memberlist
	delegate *DrpDelegate
	registry *registry.Registry

	updateMu  sync.Mutex
	leaveOnce sync.Once
	leaveErr  error

	metaMu   sync.RWMutex
	nodeMeta map[string][]string
}

const removeBroadcastAttempts = 5

func New(cfg MeshConfig, reg *registry.Registry) *Mesh {
	delegate := NewDrpDelegate(cfg.NodeID, reg)
	if cfg.BindAddr == "" {
		cfg.BindAddr = "0.0.0.0"
	}

	m := &Mesh{
		config:   cfg,
		delegate: delegate,
		registry: reg,
		nodeMeta: make(map[string][]string),
	}
	delegate.mesh = m
	return m
}

func (m *Mesh) Create() error {
	mlCfg := memberlist.DefaultLANConfig()
	mlCfg.Name = m.config.NodeID
	mlCfg.BindAddr = m.config.BindAddr
	mlCfg.BindPort = m.config.BindPort
	mlCfg.AdvertisePort = m.config.BindPort
	mlCfg.Delegate = m.delegate
	mlCfg.Events = m.delegate
	mlCfg.LogOutput = io.Discard

	list, err := memberlist.Create(mlCfg)
	if err != nil {
		return err
	}
	m.list = list

	m.delegate.SetBroadcastQueue(&memberlist.TransmitLimitedQueue{
		NumNodes:       func() int { return m.list.NumMembers() },
		RetransmitMult: mlCfg.RetransmitMult,
	})

	return nil
}

func (m *Mesh) Join(peers []string) (int, error) {
	return m.list.Join(peers)
}

func (m *Mesh) UpdateNodeMeta(nodeID string, hostnames []string) {
	m.metaMu.Lock()
	m.nodeMeta[nodeID] = hostnames
	m.metaMu.Unlock()
}

func (m *Mesh) RemoveNodeMeta(nodeID string) {
	m.metaMu.Lock()
	delete(m.nodeMeta, nodeID)
	m.metaMu.Unlock()
}

func (m *Mesh) Leave(timeout time.Duration) error {
	if m.list == nil {
		return nil
	}

	m.leaveOnce.Do(func() {
		for _, svc := range m.registry.LocalServices() {
			m.UnregisterService(svc.Hostname)
		}

		if err := m.list.Leave(timeout); err != nil {
			m.leaveErr = err
			return
		}
		m.leaveErr = m.list.Shutdown()
	})

	return m.leaveErr
}

func (m *Mesh) RegisterService(alias, hostname string) {
	m.registry.Register(hostname, m.config.NodeID, alias, true)
	m.delegate.BroadcastServiceUpdate(&drppb.ServiceUpdate{
		NodeId:     m.config.NodeID,
		Action:     "add",
		ProxyAlias: alias,
		Hostname:   hostname,
	})
	m.updateMu.Lock()
	_ = m.list.UpdateNode(5 * time.Second)
	m.updateMu.Unlock()
}

func (m *Mesh) UnregisterService(hostname string) {
	info, found := m.registry.Lookup(hostname)
	m.registry.Unregister(hostname)

	proxyAlias := ""
	if found {
		proxyAlias = info.ProxyAlias
	}

	m.delegate.BroadcastServiceUpdate(&drppb.ServiceUpdate{
		NodeId:     m.config.NodeID,
		Action:     "remove",
		ProxyAlias: proxyAlias,
		Hostname:   hostname,
	})
	for i := 1; i < removeBroadcastAttempts; i++ {
		m.delegate.BroadcastServiceUpdate(&drppb.ServiceUpdate{
			NodeId:     m.config.NodeID,
			Action:     "remove",
			ProxyAlias: proxyAlias,
			Hostname:   hostname,
		})
	}
	m.updateMu.Lock()
	_ = m.list.UpdateNode(5 * time.Second)
	m.updateMu.Unlock()
}

func (m *Mesh) Lookup(hostname string) (registry.ServiceInfo, bool) {
	info, found := m.registry.Lookup(hostname)
	if !found || info.IsLocal || m.list == nil {
		return info, found
	}

	for _, member := range m.list.Members() {
		if member.Name != info.NodeID {
			continue
		}

		m.metaMu.RLock()
		ns, ok := m.nodeMeta[member.Name]
		m.metaMu.RUnlock()
		if !ok {
			return info, true
		}

		for _, h := range ns {
			if h == hostname {
				return info, true
			}
		}

		m.registry.Unregister(hostname)
		return registry.ServiceInfo{}, false
	}

	m.registry.Unregister(hostname)
	return registry.ServiceInfo{}, false
}

func (m *Mesh) SetQuicAddr(addr string) {
	m.delegate.SetQuicAddr(addr)
}

func (m *Mesh) Members() []*memberlist.Node {
	return m.list.Members()
}

func (m *Mesh) LocalAddr() string {
	node := m.list.LocalNode()
	return fmt.Sprintf("%s:%d", node.Addr, node.Port)
}
