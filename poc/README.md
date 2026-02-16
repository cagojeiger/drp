# drp POC — Python

Proof-of-concept for the drp distributed reverse proxy mesh architecture.

Validates: server mesh + broadcast discovery + multi-hop relay.

## Requirements

- Python 3.12+ (stdlib only, zero dependencies)
- curl (for testing)

## Quick Start

```bash
bash poc/run_test.sh
```

This starts 3 servers + 1 client + 1 local backend, runs 4 tests (H1/H2/H3/F5), and cleans up.

## Architecture

```
User → curl :8003 -H "Host: myapp.example.com"
         │
         ▼
      drps-C (:8003/:9003)      no client here
         │ mesh
         ▼
      drps-B (:8002/:9002)      no client here
         │ mesh
         ▼
      drps-A (:8001/:9001)  ←── drpc (myapp)
         │ work conn                │
         ▼                          ▼
      HTTP response          localhost:5000
```

## Manual Run

**Terminal 1** — Local backend:
```bash
python3 -m http.server 5000 --directory /tmp
```

**Terminal 2** — Server A (drpc connects here):
```bash
python3 poc/drps.py --node-id A --http-port 8001 --control-port 9001 -v
```

**Terminal 3** — Server B (peers to A):
```bash
python3 poc/drps.py --node-id B --http-port 8002 --control-port 9002 --peers localhost:9001 -v
```

**Terminal 4** — Server C (peers to B):
```bash
python3 poc/drps.py --node-id C --http-port 8003 --control-port 9003 --peers localhost:9002 -v
```

**Terminal 5** — Client:
```bash
python3 poc/drpc.py --server localhost:9001 --alias myapp --hostname myapp.example.com --local localhost:5000 -v
```

**Terminal 6** — Test:
```bash
# H1: local hit (A has client)
curl -H "Host: myapp.example.com" http://localhost:8001

# H2: 1-hop relay (B → A)
curl -H "Host: myapp.example.com" http://localhost:8002

# H3: 2-hop relay (C → B → A)
curl -H "Host: myapp.example.com" http://localhost:8003

# F5: unknown host → 502
curl -H "Host: unknown.example.com" http://localhost:8002
```

## Files

| File | Lines | Purpose |
|------|-------|---------|
| `protocol.py` | ~210 | TLV+JSON wire protocol, message constructors, pipe relay |
| `mesh.py` | ~280 | MeshManager: peer connections, WhoHas/IHave broadcast, relay |
| `drpc.py` | ~267 | Client: login, register proxy, spawn work connections |
| `drps.py` | ~270 | Server: HTTP listener, control port dispatcher, mesh integration |
| `run_test.sh` | ~140 | Automated test for H1/H2/H3/F5 scenarios |

## Test Scenarios

| Test | Scenario | Route | Expected |
|------|----------|-------|----------|
| H1 | Local hit | User → A → drpc → local | 200 |
| H2 | Remote 1-hop | User → B → mesh(A) → drpc → local | 200 |
| H3 | Partial mesh 2-hop | User → C → B → A → drpc → local | 200 |
| F5 | Unknown host | User → B → broadcast timeout | 502 |

## Logs

When running `run_test.sh`, logs are written to `poc/.logs/`:
- `drps-A.log`, `drps-B.log`, `drps-C.log` — server logs
- `drpc.log` — client log
- `local.log` — backend server log
