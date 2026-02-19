# drp — Distributed Reverse Proxy

A lightweight, distributed reverse proxy for exposing local HTTP/HTTPS services behind NAT to the public internet.

Inspired by [frp](https://github.com/fatedier/frp), rebuilt from scratch with a distributed-first, minimal design.

## What It Does

```
Internet User                          Behind NAT
     |                                      |
     |  curl https://myapp.example.com      |
     |                                      |
     v                                      |
+---------+     +--------+     +---------+  |  +-------------+
|   LB    | --> | drps   | --> | drpc    | -+-> | localhost   |
| :80/443 |     | server |     | client  |     | :3000       |
+---------+     +--------+     +---------+     +-------------+
```

**drpc** (client) runs behind NAT, connects outbound to **drps** (server) via LB. Users access your local service via a public hostname — drps routes traffic by HTTP `Host` header or TLS SNI.

## Key Features

- **HTTP/HTTPS only** — Routes by `Host` header (:80) and TLS SNI (:443). Covers HTTP, WebSocket, gRPC, h2, and all TLS-based protocols.
- **Zero external dependencies** — No Redis, no etcd. Servers form a mesh and discover services via broadcast.
- **Distributed** — Multiple drps servers behind a load balancer. Servers communicate via mesh, no shared state needed.
- **Minimal** — Small codebase. No plugins, no dashboards, no complex config files.
- **Infrastructure neutral** — Works with any LB, any environment (K8s, Docker, bare metal).
- **Technology agnostic** — Architecture is not tied to any specific language or library.
- **TLS passthrough** — SNI routing on :443 without terminating TLS. End-to-end encryption preserved.

## How It Works

1. **drpc** connects to **drps** via LB (single connection)
2. **drpc** registers a service: `alias=myapp, hostname=myapp.example.com`
3. User visits `myapp.example.com` → LB → any drps
4. If drps has the client locally → direct connection
5. If not → **broadcast** "who has myapp?" → mesh peer responds → **relay** through mesh

### Mesh + Broadcast + Relay

```
drps-A ◄──── drpc (myapp)     drps-A has the client
  │
  mesh                         User hits drps-B:
  │                              1. drps-B broadcasts "who has myapp?"
drps-B ◄──── user request       2. drps-A responds "I have it!"
  │                              3. drps-B relays data through mesh
  mesh
  │                            Relay is intra-cluster: sub-ms latency
drps-C
```

### HA (Optional)

By default, drpc connects to 1 server. Mesh handles routing.

Optionally, enable HA for fault tolerance:

```bash
drp client --server lb.example.com:9000 --ha-connections 2
```

### Protocol Support

| Protocol | Port | Routing |
|----------|------|---------|
| HTTP/1.x, WebSocket | :80 | `Host` header |
| HTTPS, WSS, gRPC, h2 | :443 | TLS SNI |

**Not supported**: Raw TCP (no hostname info), UDP.

## Architecture

```
            Public Internet
                  |
           +------+------+
           |     LB      |
           | :80 :443    |  <-- user traffic
           | :9000       |  <-- drpc control
           +--+-----+--+-+
              |     |  |
           drps-A  B   C     <-- mesh connected
              |  \/ |  |
              |  /\ |  |
              mesh mesh mesh  <-- broadcast + relay
              |     |  |
           drpc-1  2   3     <-- behind NAT, each connects to 1 drps
              |     |  |
           local  local local
```

### Components

| Component | Role |
|-----------|------|
| **drps** (server) | Accepts user connections, routes by Host/SNI, mesh with peers |
| **drpc** (client) | Connects to 1 drps via LB, provides work connections to local services |
| **LB** | L4 load balancer (round-robin). No sticky sessions needed |

## Comparison with frp

| | frp | drp |
|---|---|---|
| Architecture | Single server | **Distributed (server mesh)** |
| Routing | Port-based (port exhaustion) | **Host/SNI (2 ports for unlimited services)** |
| External deps | None (but single server) | **None (and distributed)** |
| Protocol | TCP/UDP/HTTP/KCP/QUIC/WS/... | **HTTP/HTTPS (web traffic only)** |
| HA | None | **Mesh routing + optional multi-connect** |
| Deployment | Single server only | **Any LB, any infra** |

## Design Decisions

See [docs/adr/](docs/adr/) for Architecture Decision Records:

| ADR | Decision |
|-----|----------|
| [001](docs/adr/001-scope-and-philosophy.md) | HTTP/HTTPS only, zero external deps |
| [002](docs/adr/002-host-sni-routing.md) | Host header + TLS SNI routing |
| [003](docs/adr/003-server-mesh-and-discovery.md) | Server mesh, broadcast discovery, relay |
| [004](docs/adr/004-protocol-and-messages.md) | TLV + JSON protocol |
| [005](docs/adr/005-mesh-transport-quic.md) | QUIC for mesh transport (multiplexing) |

## License

MIT
