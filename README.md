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
                    |
                  Redis
              (routing table)
```

**drpc** (client) runs behind NAT, connects outbound to **drps** (server) on a public VPS. Users access your local service via a public hostname — drps routes traffic by HTTP `Host` header or TLS SNI.

## Key Features

- **HTTP/HTTPS only** — Routes by `Host` header (:80) and TLS SNI (:443). Covers HTTP, WebSocket, gRPC, h2, and all TLS-based protocols.
- **Distributed** — Multiple drps nodes behind a load balancer, Redis for shared routing state. Servers can be added/removed freely.
- **HA Connections** — drpc connects to multiple drps nodes (default 2) for high availability and zero-downtime failover. Inspired by [Cloudflare Tunnel](https://github.com/cloudflare/cloudflared).
- **Minimal** — ~750 lines of Go. No plugins, no dashboards, no complex config files.
- **LB-friendly** — Standard L4 load balancer in front. No sticky sessions required. Works natively with Kubernetes.
- **TLS passthrough** — SNI routing on :443 without terminating TLS. End-to-end encryption preserved.

## How It Works

1. **drpc** connects to **drps** via TLS over TCP (through LB)
2. **drpc** registers a proxy: `alias=myapp, hostname=myapp.example.com`
3. **drps** stores the route in Redis
4. User visits `myapp.example.com` → LB → any drps → Redis lookup → work connection to drpc → `localhost:3000`

### HA Connections (Multi-Connect)

drpc maintains connections to multiple drps nodes simultaneously:

```
drpc --ha-connections 2

  conn-1 --> LB:7000 --> drps-A  (assigned by LB)
  conn-2 --> LB:7000 --> drps-B  (assigned by LB)
```

- User hits drps-A → drpc is there → direct. No relay.
- User hits drps-B → drpc is there → direct. No relay.
- If drps-A goes down → drps-B continues. drpc reconnects drps-A in background.

### Protocol Support

| Protocol | Port | Routing |
|----------|------|---------|
| HTTP/1.0, 1.1 | :80 | `Host` header |
| WebSocket (ws) | :80 | `Host` header |
| HTTPS | :443 | TLS SNI |
| WebSocket (wss) | :443 | TLS SNI |
| gRPC over TLS | :443 | TLS SNI |
| HTTP/2 (h2) | :443 | TLS SNI |

**Not supported**: Raw TCP (no hostname info), UDP.

## Quick Start

### Server (public VPS)

```bash
drp server \
    --control-addr  0.0.0.0:7000 \
    --public-addr   0.0.0.0:80 \
    --tls-addr      0.0.0.0:443 \
    --redis-addr    localhost:6379 \
    --node-id       drps-1
```

### Client (behind NAT)

```bash
drp client \
    --server        lb.example.com:7000 \
    --api-key       sk-abc123 \
    --alias         myapp \
    --hostname      myapp.example.com \
    --local         127.0.0.1:3000 \
    --ha-connections 2
```

### Test

```bash
curl http://myapp.example.com
# → LB → drps → drpc → localhost:3000
```

## Architecture

```
            Public Internet
                  |
           +------+------+
           |     LB      |
           | :80 :443    |  <-- user traffic
           | :7000       |  <-- drpc control (TLS)
           +--+-----+--+-+
              |     |  |
           drps-A  B   C     <-- stateless, behind LB
              |     |  |
              +--+--+--+
                 |
               Redis         <-- routing table, auth, node registry
                 |
           +-----+-----+
           |     |     |
         drpc-1  2     3     <-- behind NAT, HA connections to multiple drps
           |     |     |
        local  local  local
```

### Components

| Component | Role |
|-----------|------|
| **drps** (server) | Accepts user connections, routes by Host/SNI, manages work connections |
| **drpc** (client) | Connects to drps, provides work connections to local services |
| **Redis** | Shared routing table, API key auth, node registry |
| **LB** | L4 load balancer (round-robin). No sticky sessions needed |

## Comparison with frp

| | frp | drp |
|---|---|---|
| Code | 36,000 lines, 238 files | **~750 lines, 10 files** |
| Architecture | Single server | **Distributed (Redis + multi-node)** |
| Routing | Port-based (port exhaustion) | **Host/SNI (2 ports for unlimited services)** |
| Encryption | Custom CryptoRW + TLS + snappy | **TLS (standard library)** |
| Protocol | TCP/UDP/HTTP/KCP/QUIC/WS/... | **HTTP/HTTPS (web traffic only)** |
| HA | None | **Multi-connect + auto-reconnect** |
| Deployment | Single server only | **K8s / LB native** |
| Config | Complex TOML/YAML | **CLI flags** |

## Dependencies

| Dependency | Purpose |
|------------|---------|
| Go standard library | net, io, crypto/tls, http, bufio, encoding/json |
| `github.com/redis/go-redis` | Redis client |
| `github.com/hashicorp/yamux` | TCP multiplexing |

## Design Decisions

See [docs/adr/](docs/adr/) for Architecture Decision Records.

## License

MIT