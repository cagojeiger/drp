# drp — Distributed Reverse Proxy

> frp(Fast Reverse Proxy)의 핵심만 추출하여 분산 + 스마트 라우팅을 더한 경량 리버스 프록시

**이름**: drp = **D**istributed **R**everse **P**roxy
**상태**: 설계 완료, 구현 대기

---

## 1. 프로젝트 배경

### 1.1 왜 만드는가

frp(fatedier/frp)는 NAT 뒤 서비스를 외부에 노출하는 훌륭한 도구지만:
- **36,000줄, 238개 Go 파일** — 너무 크고 복잡
- **단일 서버 한계** — ControlManager, proxy.Manager, workConnCh 모두 in-memory. 분산 불가
- **포트 기반 라우팅** — 서비스마다 포트 하나씩 차지. 포트 고갈 문제
- **자체 암호화 레이어** — CryptoReadWriter + Compression + ContextConn 래핑이 Go의 splice() zero-copy를 깨뜨림
- **goroutine 무제한 생성** — MaxConnection 제한 없음

drp는 이 문제들을 해결한다.

### 1.2 핵심 원칙

| 원칙 | 설명 |
|------|------|
| **최소주의** | ~850줄. 한 가지만 잘 한다 (Unix 철학) |
| **분산 우선** | 단일 서버가 아닌, 수평 확장이 기본 설계 |
| **인프라 위임** | 암호화는 WireGuard, TLS는 앞단/뒷단에 위임 |
| **투명한 파이프** | 프록시는 바이트를 이해하지 않고, 그냥 흘려보낸다 |

### 1.3 frp 대비 차이점

| | frp | drp |
|---|---|---|
| 코드 | 36,000줄, 238파일 | **~850줄, 10파일** |
| 아키텍처 | 단일 서버 | **분산 (Redis + 멀티 노드)** |
| 라우팅 | 포트 기반 (포트 고갈) | **HTTP Host / TLS SNI (포트 1~2개)** |
| 암호화 | 자체 구현 (CryptoRW + TLS + snappy) | **인프라 위임 (WireGuard)** |
| 프로토콜 지원 | TCP/UDP/HTTP/KCP/QUIC/WS 등 | **TCP (HTTP 라우팅)** |
| 프록시 내 암호화 코드 | 수천 줄 | **0줄** |
| 보안 모델 | 앱 레벨 토큰 | **네트워크 격리 (WG) + API Key ACL** |
| 스케일링 | 불가 | **자동 분산 (LB + Redis)** |
| 설정 | TOML/YAML 복잡한 설정 | **CLI 플래그 몇 개** |

---

## 2. frp 코드베이스 분석 결과

### 2.1 아키텍처

- 컨트롤 플레인 / 데이터 플레인 분리
- 메시지 디스패처 패턴 (sendCh buffer=100)
- 메시지 포맷: `[1 byte type][4 byte length][JSON body]`

### 2.2 핵심 파일 (분석 완료)

| 파일 | 역할 |
|------|------|
| `server/service.go` | 서버 메인 서비스 |
| `server/control.go` | 서버 제어 연결, work conn pool |
| `server/proxy/proxy.go` | 베이스 프록시, `handleUserTCPConnection`, `libio.Join` |
| `server/proxy/tcp.go` | TCP 프록시 구현 |
| `client/service.go` | 클라이언트 서비스, 로그인 루프 |
| `client/control.go` | 클라이언트 제어, 메시지 디스패치 |
| `client/connector.go` | yamux/QUIC/TCP 연결 수립 |
| `pkg/msg/msg.go` | 메시지 타입 정의 |
| `pkg/msg/handler.go` | 메시지 디스패처 |
| `pkg/msg/ctl.go` | JSON 메시지 인코딩 |

### 2.3 핫 패스 (성능 병목)

```
user conn → ContextConn → CryptoReadWriter → Compression → RateLimit → io.CopyBuffer(16KB)
```

- `libio.Join()`이 `io.CopyBuffer`를 16KB sync.Pool 버퍼로 사용
- 연결 래핑 레이어 4개가 Go splice() zero-copy를 완전히 깨뜨림
- 결과: 1GB 전송 시 ~65,000 syscall 발생

### 2.4 Work Connection Pool

- `chan net.Conn` with `poolCount+10` buffer
- 기본 `poolCount=1` → 실질 동시 ~10-20 연결
- `MaxConnection` 제한 없음 → goroutine 무한 생성 가능

### 2.5 yamux

- fatedier의 yamux 포크 사용 (`fatedier/yamux`)
- 하드코딩된 `MaxStreamWindowSize = 6MB`

### 2.6 메모리 사용

- 연결당 ~43KB (goroutine 24KB + net.Conn 2KB + 16KB buffer + 1KB overhead)

---

## 3. 아키텍처 설계

### 3.1 전체 구성도

```
                         공개 인터넷
                              │
                    ┌─────────┴─────────┐
                    │  L4 Load Balancer  │
                    │  :80, :443        │
                    └────┬─────────┬────┘
                         │         │
              ┌──────────┴──┐  ┌──┴──────────┐
              │   drps-A    │  │   drps-B    │
              │ 10.0.0.10   │  │ 10.0.0.11   │
              │ :80 (user)  │  │ :80 (user)  │
              │ :7000 (ctl) │  │ :7000 (ctl) │
              │ :9000 (relay)│  │ :9000 (relay)│
              └──────┬──────┘  └──────┬──────┘
                     │                │
                     └───────┬────────┘
                             │
                  ┌──────────┴──────────┐
                  │   Redis 10.0.0.100  │
                  │   (WireGuard 내부)   │
                  └─────────────────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
         ┌────┴────┐   ┌────┴────┐   ┌────┴────┐
         │ drpc-1  │   │ drpc-2  │   │ drpc-3  │
         │10.0.0.20│   │10.0.0.21│   │10.0.0.22│
         │ (NAT뒤) │   │ (NAT뒤) │   │ (NAT뒤) │
         └────┬────┘   └────┬────┘   └────┬────┘
              │              │              │
         local:3000    local:8080    local:5000

         ═══ WireGuard 터널 (모든 내부 통신) ═══
```

### 3.2 포트 바인딩

| 포트 | 바인딩 | 용도 | 접근 |
|------|--------|------|------|
| `:80` | `0.0.0.0` | HTTP 사용자 트래픽 | 공개 |
| `:443` | `0.0.0.0` | HTTPS 사용자 트래픽 (SNI) | 공개 |
| `:7000` | `10.0.0.x` (WG) | drpc 제어 연결 | WireGuard만 |
| `:9000` | `10.0.0.x` (WG) | 노드 간 relay | WireGuard만 |

### 3.3 구성 요소

```
┌─────────────────────────────────────────────────┐
│                    drp server                    │
├─────────────────────────────────────────────────┤
│                                                  │
│  Public Listeners (:80, :443)                    │
│  ├─ :80  → HTTP Host 라우팅                      │
│  └─ :443 → TLS SNI 라우팅                        │
│                                                  │
│  Control Listener (:7000, WG only)               │
│  ├─ Login (API Key 검증)                         │
│  ├─ NewProxy (alias/hostname 등록)               │
│  └─ Heartbeat (Ping/Pong)                        │
│                                                  │
│  Cluster Listener (:9000, WG only)               │
│  └─ Inter-server work conn relay                 │
│                                                  │
│  Internal                                        │
│  ├─ Registry (Redis)   — 라우팅 테이블            │
│  ├─ Auth (Redis)       — API Key + ACL           │
│  ├─ Pool               — work conn pool (로컬)   │
│  └─ Cluster            — 노드 간 통신             │
│                                                  │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│                    drp client                    │
├─────────────────────────────────────────────────┤
│                                                  │
│  Connector                                       │
│  ├─ TCP 연결 (WireGuard 경유)                    │
│  └─ yamux 멀티플렉싱                              │
│                                                  │
│  Control                                         │
│  ├─ Login (API Key 전송)                         │
│  ├─ NewProxy (alias/hostname 등록)               │
│  └─ Heartbeat                                    │
│                                                  │
│  Worker                                          │
│  ├─ ReqWorkConn 수신 → 새 yamux stream           │
│  └─ 로컬 서비스 연결 → relay                      │
│                                                  │
└─────────────────────────────────────────────────┘
```

---

## 4. 프로토콜 설계

### 4.1 메시지 포맷

```
┌──────────┬──────────────┬──────────────────┐
│ Type     │ Length       │ Body             │
│ 1 byte   │ 4 bytes (BE) │ JSON (variable)  │
└──────────┴──────────────┴──────────────────┘
```

### 4.2 메시지 타입

```
제어 채널 (drpc ↔ drps):

  'L' Login            drpc → drps   인증 요청
  'l' LoginResp        drps → drpc   인증 응답
  'P' NewProxy         drpc → drps   프록시 등록
  'p' NewProxyResp     drps → drpc   등록 결과
  'R' ReqWorkConn      drps → drpc   work conn 요청
  'W' NewWorkConn      drpc → drps   work conn 제공
  'S' StartWorkConn    drps → drpc   relay 시작
  'H' Ping             drpc → drps   heartbeat
  'h' Pong             drps → drpc   heartbeat 응답

노드 간 (drps ↔ drps):

  'Q' RelayReq         drps → drps   원격 work conn 요청
  'q' RelayResp        drps → drps   응답 후 TCP relay 전환
```

### 4.3 메시지 구조체

```go
// --- 인증 ---
type Login struct {
    APIKey string `json:"api_key"`
    RunID  string `json:"run_id"`
}

type LoginResp struct {
    RunID string `json:"run_id"`
    Error string `json:"error,omitempty"`
}

// --- 프록시 등록 ---
type NewProxy struct {
    Alias      string   `json:"alias"`
    Hostname   string   `json:"hostname"`
    LocalAddr  string   `json:"local_addr"`
    AllowedIPs []string `json:"allowed_ips,omitempty"`
}

type NewProxyResp struct {
    Alias string `json:"alias"`
    URL   string `json:"url"`
    Error string `json:"error,omitempty"`
}

// --- Work Connection ---
type ReqWorkConn struct{}

type NewWorkConn struct {
    RunID string `json:"run_id"`
}

type StartWorkConn struct {
    Alias string `json:"alias"`
}

// --- Heartbeat ---
type Ping struct{}
type Pong struct{}

// --- 노드 간 Relay ---
type RelayReq struct {
    Alias string `json:"alias"`
}

type RelayResp struct {
    Error string `json:"error,omitempty"`
}
```

### 4.4 연결 수립 흐름

```
drpc                                          drps
 │                                             │
 ├── [TCP connect to 10.0.0.x:7000] ─────────→│
 │   (WireGuard 경유)                           │
 │                                             │
 │   === yamux session 수립 ===                 │
 │                                             │
 ├── Login{APIKey, RunID:""}  ────────────────→│ API Key 검증 + RunID 생성
 │← LoginResp{RunID:"run-a1b2c3"}  ──────────│
 │                                             │
 ├── NewProxy{Alias, Hostname, LocalAddr} ───→│ Redis에 등록 + ACL 검증
 │← NewProxyResp{Alias, URL}  ───────────────│
 │                                             │
 │   === 주기적 Heartbeat ===                   │
 ├── Ping{} ──────────────────────────────────→│
 │← Pong{} ───────────────────────────────────│
```

### 4.5 데이터 릴레이 흐름

```
Case 1: 로컬 hit (drpc가 같은 drps에 연결)

user                drps-A                          drpc
 │                   │                               │
 ├── TCP :80 ───────→│ HTTP Host peek                │
 │                   ├─ Redis 조회 → 로컬!            │
 │                   ├── ReqWorkConn{} ─────────────→│
 │                   │←── NewWorkConn{RunID} ────────│ yamux stream open
 │                   ├── StartWorkConn{Alias} ──────→│ local:3000 연결
 │←══ io.Copy ══════════════════════════════════════→│

Case 2: 원격 hit (drpc가 다른 drps에 연결)

user                drps-B              drps-A              drpc
 │                   │                   │                   │
 ├── TCP :80 ───────→│ Host peek         │                   │
 │                   ├─ Redis → 원격!    │                   │
 │                   ├── RelayReq ──────→│                   │
 │                   │                   ├── ReqWorkConn ──→│
 │                   │                   │←── NewWorkConn ──│
 │                   │                   ├── StartWorkConn →│
 │                   │←── RelayResp ────│                   │
 │←══ relay ════════════ relay ═══════════ relay ═══════════→│
```

---

## 5. 라우팅

### 5.1 라우팅 방식

| 포트 | 방식 | 추출 대상 | 프로토콜 호환성 |
|------|------|----------|---------------|
| `:80` | HTTP Host 헤더 | 첫 HTTP 요청의 `Host:` | HTTP/1.0, 1.1, WebSocket |
| `:443` | TLS SNI | ClientHello의 SNI 필드 | HTTPS, h2, gRPC, WSS, 모든 TLS |

### 5.2 :80 라우팅 (HTTP Host)

```go
func (s *Server) handleHTTPConn(conn net.Conn) {
    br := bufio.NewReader(conn)
    req, err := http.ReadRequest(br)
    hostname := stripPort(req.Host)
    route := s.registry.GetByHostname(hostname)
    workConn := s.getWorkConn(route)
    req.Write(workConn)
    replay(br, workConn)
    relay(workConn, conn)
}
```

### 5.3 :443 라우팅 (TLS SNI)

```go
func (s *Server) handleTLSConn(conn net.Conn) {
    hostname, initBytes := peekSNI(conn)
    route := s.registry.GetByHostname(hostname)
    workConn := s.getWorkConn(route)
    workConn.Write(initBytes)
    relay(workConn, conn)
}
```

### 5.4 프로토콜 호환성 매트릭스

| 프로토콜 | :80 | :443 |
|---------|-----|------|
| HTTP/1.0, 1.1 | ✅ | - |
| HTTP/2 over TLS (h2) | - | ✅ |
| HTTP/2 cleartext (h2c upgrade) | ✅ | - |
| WebSocket (ws) | ✅ | - |
| WebSocket over TLS (wss) | - | ✅ |
| gRPC over TLS | - | ✅ |
| 임의 TLS 프로토콜 | - | ✅ |

---

## 6. 보안 설계

### 6.1 4계층 보안 모델

```
Layer 0: 네트워크 격리
├─ 제어 포트 (:7000) → WireGuard에서만 접근
├─ Redis (:6379) → WireGuard에서만 접근
└─ 노드 간 통신 (:9000) → WireGuard에서만 접근

Layer 1: 전송 암호화
├─ user ↔ drps → TLS (SNI 패스스루 or 앞단 처리)
└─ drps ↔ drpc → WireGuard (커널 레벨 암호화)

Layer 2: 인증 (Authentication)
├─ drpc → drps → API Key
└─ WireGuard → 공개키 인증 (피어 단위)

Layer 3: 인가 (Authorization)
├─ API Key → 허용된 alias/hostname만 등록 가능
└─ 프록시별 IP whitelist (선택)

Layer 4: 격리
├─ alias 충돌 방지 (Redis NX, 선점 방식)
└─ 노드 장애 격리 (TTL 기반 자동 정리)
```

### 6.2 공격 벡터 대응

| # | 공격 | 대응 |
|---|------|------|
| 1 | 비인가 drpc 연결 | 제어 포트 WG 격리 + API Key |
| 2 | hostname 하이재킹 | API Key별 alias/hostname ACL |
| 3 | user↔drps 트래픽 도청 | SNI 패스스루 (end-to-end TLS) |
| 4 | drps↔drpc 트래픽 도청 | WireGuard 암호화 |
| 5 | Redis 침투/변조 | WG 격리 + Redis AUTH |
| 6 | drps 노드 침해 | SNI 패스스루 (내용 복호화 불가) |
| 7 | 프록시 무단 접근 | 프록시별 IP whitelist |
| 8 | DDoS | LB + rate limiting (인프라 레벨) |

### 6.3 API Key + ACL

```
Redis 저장:
  HSET apikey:sk-abc123
       aliases    "myapp,myapi"
       hostnames  "*.myteam.example.com"
       created_at "2025-01-01T00:00:00Z"

검증 흐름:
  1. Login → API Key 존재 확인
  2. NewProxy → alias + hostname이 ACL 범위 내인지 확인
  3. 실패 → Error 응답 + 연결 종료
```

---

## 7. 분산 설계

### 7.1 공유 상태 (Redis)

```
# 라우트 등록
HSET route:myapp.example.com
     alias   "myapp"
     node    "drps-A"
     run_id  "run-a1b2c3"

# 노드 heartbeat
SET node:drps-A "10.0.0.10:9000" EX 30

# API Key
HSET apikey:sk-abc123
     aliases   "myapp,myapi"
     hostnames "*.myteam.example.com"
```

### 7.2 노드 간 Work Connection Relay

```go
func (s *Server) getWorkConn(route *RouteInfo) (net.Conn, error) {
    if route.Node == s.nodeID {
        return s.localPool.Get(route.Alias)  // 로컬
    }
    return s.cluster.RequestWorkConn(route.Node, route.Alias)  // 원격 relay
}
```

### 7.3 노드 추가/제거

```
추가: WG peer 추가 → drp server 시작 → Redis 자동 등록 → LB에 추가 → 즉시 가동
제거: LB에서 제거 → 프로세스 종료 → TTL 만료로 자동 정리 → drpc 재연결
```

### 7.4 장애 처리

- **drps 장애**: TTL 만료 → route 자동 정리, drpc 다른 노드로 재연결, LB health check
- **Redis 장애**: Sentinel/Cluster HA, 기존 연결 유지, 신규 등록만 불가
- **drpc 장애**: heartbeat timeout → route 정리, 재시작 시 재연결 + 재등록

---

## 8. 성능 특성

### 8.1 데이터 릴레이

- 핫 패스: `user conn → io.Copy → work conn` (양방향)
- splice() zero-copy 가능 (WireGuard 내부는 raw TCP)
- 프록시 코드 내 암호화/압축 없음, wrapper 레이어 없음

### 8.2 연결당 자원

| 자원 | 크기 |
|------|------|
| Goroutine (3개) | ~24KB |
| net.Conn (2개) | ~2KB |
| yamux stream state | ~1KB |
| **합계** | **~27KB/연결** |

### 8.3 vs frp

| 항목 | frp | drp |
|------|-----|-----|
| 데이터 릴레이 | io.CopyBuffer 16KB + 4개 wrapper | io.Copy (splice 가능) |
| syscall (1GB) | ~65,000 | ~수천 (splice) |
| 메모리/연결 | ~43KB | ~27KB |
| wrapper 레이어 | TLS+ContextConn+Crypto+Compress | 없음 |

### 8.4 예상 한계

| 서버 스펙 | 동시 연결 | 병목 |
|----------|----------|------|
| 2GB RAM | ~50,000 | 메모리 |
| 8GB RAM | ~200,000 | file descriptor |
| 32GB RAM | ~500,000 | OS 튜닝 필요 |

---

## 9. 코드 구조 (계획)

```
drp/
├── main.go              # CLI 파싱, server/client 모드    (~60줄)
├── msg.go               # 메시지 정의 + 직렬화/역직렬화    (~100줄)
│
│   ── 서버 ──
├── server.go            # Accept, Login, 라우팅, relay     (~230줄)
├── auth.go              # API Key 검증 + ACL              (~50줄)
├── registry.go          # Redis 기반 라우팅 테이블          (~90줄)
├── cluster.go           # 노드 간 통신 + relay             (~100줄)
│
│   ── 클라이언트 ──
├── client.go            # Connect, Login, work conn        (~130줄)
│
│   ── 공용 ──
├── relay.go             # io.Copy 양방향 + peek replay     (~40줄)
├── pool.go              # work connection pool             (~50줄)
│
                           ──────────────────────
                           합계: ~850줄
```

---

## 10. CLI 설계

### 10.1 서버

```bash
drp server \
    --control-addr  10.0.0.1:7000 \
    --public-addr   0.0.0.0:80 \
    --tls-addr      0.0.0.0:443 \
    --cluster-addr  10.0.0.1:9000 \
    --redis-addr    10.0.0.100:6379 \
    --redis-pass    "redis-password" \
    --node-id       drps-A
```

### 10.2 클라이언트

```bash
drp client \
    --server    10.0.0.1:7000 \
    --api-key   sk-abc123 \
    --alias     myapp \
    --hostname  myapp.example.com \
    --local     127.0.0.1:3000
```

---

## 11. 인프라 설정

### 11.1 WireGuard

```ini
# VPS (drps) — /etc/wireguard/wg0.conf
[Interface]
PrivateKey = <서버 비밀키>
Address = 10.0.0.1/24
ListenPort = 51820

[Peer]  # drpc-1
PublicKey = <클라이언트1 공개키>
AllowedIPs = 10.0.0.20/32
```

```ini
# 로컬 (drpc) — /etc/wireguard/wg0.conf
[Interface]
PrivateKey = <클라이언트 비밀키>
Address = 10.0.0.20/24

[Peer]
PublicKey = <서버 공개키>
Endpoint = <VPS공인IP>:51820
AllowedIPs = 10.0.0.0/24
PersistentKeepalive = 25
```

### 11.2 DNS

```
*.example.com → L4 LB 공인 IP (와일드카드 A 레코드)
```

---

## 12. 사용 예시

### 12.1 기본 HTTP

```bash
# 서버 (VPS)
$ drp server --control-addr 10.0.0.1:7000 --public-addr 0.0.0.0:80

# 클라이언트 (로컬)
$ drp client --server 10.0.0.1:7000 --api-key sk-abc123 \
             --alias myapp --hostname myapp.example.com \
             --local 127.0.0.1:3000

# 외부 접속
$ curl http://myapp.example.com
# → LB → drps:80 → Host 라우팅 → WireGuard → drpc → localhost:3000
```

### 12.2 HTTPS (SNI 패스스루)

```bash
# 로컬에서 caddy로 TLS 종료
$ caddy reverse-proxy --from :443 --to :3000

# drp 클라이언트
$ drp client --server 10.0.0.1:7000 --api-key sk-abc123 \
             --alias myapp --hostname myapp.example.com \
             --local 127.0.0.1:443

# 외부 HTTPS
$ curl https://myapp.example.com
# → LB → drps:443 → SNI → WireGuard → drpc → caddy:443 → app:3000
```

---

## 13. 의존성

| 의존성 | 용도 |
|--------|------|
| Go 표준 라이브러리 | net, io, http, bufio, encoding/json |
| `github.com/redis/go-redis` | Redis 클라이언트 |
| `github.com/hashicorp/yamux` | TCP 멀티플렉싱 |
| WireGuard (인프라) | 전송 암호화 (코드 의존성 아님) |
| Redis (인프라) | 공유 상태 (코드 의존성 아님) |

---

## 14. 향후 확장 가능성

| 기능 | 난이도 | 방법 |
|------|--------|------|
| 메트릭/모니터링 | 쉬움 | Prometheus exporter |
| 접속 로그 | 쉬움 | relay 시작/종료 로그 |
| 대시보드 | 중간 | Redis 데이터 기반 웹 UI |
| TCP 포트 직접 할당 | 쉬움 | 포트 기반 라우팅 병행 |
| 대역폭 제한 | 쉬움 | rate-limited reader/writer |
| 자동 TLS (Let's Encrypt) | 중간 | ACME 클라이언트 |
| UDP 지원 | 중간 | UDP relay 모듈 |

---

## 15. 설계 결정 기록

| 결정 | 선택 | 이유 |
|------|------|------|
| 암호화 방식 | WireGuard (mTLS 아닌) | splice() zero-copy 보존, 코드 0줄 |
| 라우팅 방식 | Host/SNI (포트 기반 아닌) | 포트 고갈 방지, 분산에 유리 |
| 상태 저장 | Redis (etcd 아닌) | 간단, 충분한 성능, 운영 경험 풍부 |
| 멀티플렉싱 | yamux | 검증된 라이브러리, frp에서도 사용 |
| 프로토콜 포맷 | JSON (protobuf 아닌) | 제어 메시지는 빈도 낮음, 디버깅 용이 |
| 단일 리전 우선 | 멀티 리전 추후 | 현재 필요 없음, 설계는 블로킹 안 함 |

---

## 16. 참고 소스 (frp 원본)

분석에 사용된 frp 원본 코드 위치:
- 프로젝트: `/Users/kangheeyong/project/frp/` (fatedier/frp 클론)
- 외부 의존성: `/Users/kangheeyong/go/pkg/mod/github.com/fatedier/golib@v0.5.1/`

---

## 17. 다음 단계

1. **Go 모듈 초기화** (`go mod init github.com/cagojeiger/drp`)
2. **msg.go** — 메시지 타입 + 직렬화 구현
3. **relay.go** — io.Copy 양방향 + peek replay
4. **pool.go** — work connection pool
5. **auth.go** — API Key + ACL (Redis)
6. **registry.go** — 라우팅 테이블 (Redis)
7. **server.go** — 메인 서버 로직
8. **client.go** — 메인 클라이언트 로직
9. **cluster.go** — 노드 간 relay
10. **main.go** — CLI 진입점
11. **테스트**
12. **WireGuard 설정 문서**
