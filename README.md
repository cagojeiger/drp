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
- **No infrastructure dependencies** — No Redis, no etcd, no Consul. Servers form a SWIM+Gossip mesh and discover services automatically.
- **Distributed** — Multiple drps servers behind a load balancer. Service registry propagates via gossip protocol — no shared state needed.
- **Minimal** — Small codebase, three external libraries. No plugins, no dashboards, no complex config files.
- **Infrastructure neutral** — Works with any L4 LB, any environment (K8s, Docker, bare metal).
- **TLS passthrough** — SNI routing on :443 without terminating TLS. End-to-end encryption preserved.

## How It Works

1. **drpc** connects to **drps** via LB (TCP control connection)
2. **drpc** registers a service: `alias=myapp, hostname=myapp.example.com`
3. **drps** adds the service to its local registry and propagates via gossip to all mesh peers
4. User visits `myapp.example.com` → LB → any drps node
5. drps looks up hostname in local registry (O(1) lookup)
6. If the service is **local** → direct work connection to the drpc client
7. If the service is **remote** → QUIC relay stream to the node that owns it

### Gossip Propagation + Local Lookup + QUIC Relay

```
drps-A ◄──── drpc (myapp)       drps-A owns the service
  │
  gossip (SWIM protocol)         User hits drps-B:
  │                                1. drps-B checks local registry → myapp is on drps-A
drps-B ◄──── user request         2. drps-B opens QUIC stream to drps-A
  │                                3. drps-A requests work conn from drpc
  gossip                           4. traffic flows: user ↔ drps-B ↔ QUIC ↔ drps-A ↔ drpc ↔ localhost
  │
drps-C                           QUIC relay is intra-cluster: sub-ms latency
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
              mesh mesh mesh  <-- gossip + QUIC relay
              |     |  |
           drpc-1  2   3     <-- behind NAT, each connects to 1 drps
              |     |  |
           local  local local
```

### Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| :80 | TCP | HTTP user traffic (Host header routing) |
| :443 | TCP | HTTPS/TLS user traffic (SNI routing) |
| :9000 | TCP | drpc control (Login, NewProxy, WorkConn) |
| :9001 | QUIC/UDP | Server-to-server relay (multiplexed streams) |
| :7946 | TCP+UDP | SWIM+Gossip membership (memberlist) |

### Components

| Component | Role |
|-----------|------|
| **drps** (server) | Accepts user connections, routes by Host/SNI, gossip mesh with peers, QUIC relay |
| **drpc** (client) | Connects to 1 drps via LB, provides work connections to local services |
| **LB** | L4 load balancer (round-robin). No sticky sessions needed |

### Tech Stack

| Library | Purpose |
|---------|---------|
| [hashicorp/memberlist](https://github.com/hashicorp/memberlist) | SWIM+Gossip membership and service discovery |
| [quic-go/quic-go](https://github.com/quic-go/quic-go) | QUIC transport for server-to-server relay |
| [google/protobuf](https://pkg.go.dev/google.golang.org/protobuf) | Protocol Buffers for message serialization |

No other external dependencies. No infrastructure services required.

## Usage

### Build

```bash
make build
# produces: bin/drps, bin/drpc
```

### Server (drps)

```bash
drps \
  --node-id node-1 \
  --http :80 \
  --https :443 \
  --control :9000 \
  --quic :9001 \
  --mesh-port 7946 \
  --join node-2:7946,node-3:7946
```

### Client (drpc)

```bash
drpc \
  --server lb.example.com:9000 \
  --alias myapp \
  --hostname myapp.example.com \
  --type http \
  --local 127.0.0.1:3000
```

## Comparison with frp

| | frp | drp |
|---|---|---|
| Architecture | Single server | **Distributed (server mesh)** |
| Routing | Port-based (port exhaustion) | **Host/SNI (2 ports for unlimited services)** |
| Service discovery | N/A (single server) | **SWIM+Gossip (O(1) lookup, auto failure detection)** |
| Server relay | N/A | **QUIC (multiplexed streams, no HoL blocking)** |
| Serialization | Custom JSON | **Protocol Buffers (schema evolution, type safety)** |
| External deps | None (but single server) | **No infra deps (3 Go libraries)** |
| Protocol | TCP/UDP/HTTP/KCP/QUIC/WS/... | **HTTP/HTTPS only** |
| HA | None | **Mesh routing (any node can serve any request)** |
| Deployment | Single server only | **Any LB, any infra** |

## Design Decisions

See [docs/adr/](docs/adr/) for Architecture Decision Records:

| ADR | Decision |
|-----|----------|
| [001](docs/adr/001-scope-and-philosophy.md) | HTTP/HTTPS only, zero infra deps, proven tech |
| [002](docs/adr/002-host-sni-routing.md) | Host header + TLS SNI routing |
| [003](docs/adr/003-server-mesh-and-discovery.md) | SWIM+Gossip membership and service discovery |
| [004](docs/adr/004-protocol-and-messages.md) | Protocol Buffers with protodelim framing |
| [005](docs/adr/005-mesh-transport-quic.md) | QUIC for server-to-server relay |

## License

MIT
