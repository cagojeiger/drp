# ADR-007: HA Connections (Multi-Connect)

## 상태
Accepted

## 컨텍스트

### 문제: 단일 연결의 한계

기존 설계에서 drpc는 drps 1대에만 연결한다. 분산 환경에서 이 구조의 문제:

1. **WebSocket 병목** — 사용자 WebSocket이 drpc가 없는 서버에 도착하면 relay가 필요. 모든 long-lived 연결이 한 서버를 경유하게 되어 부하 집중.
2. **단일 장애점** — 연결된 drps가 죽으면 서비스 전체 중단.
3. **relay 복잡도** — 노드 간 relay (:9000) 코드가 필요. 코드량 증가.

### Cloudflare Tunnel 사례

Cloudflare Tunnel(`cloudflared`)은 이 문제를 **HA 멀티 연결**로 해결한다:
- 기본값: **4개** 동시 연결 (`--ha-connections 4`)
- 각 연결은 서로 다른 edge 서버에 수립
- 한 연결이 끊기면 나머지로 서비스 계속 + 끊긴 연결은 backoff 재연결

```go
// cloudflared supervisor/supervisor.go
altsrc.NewIntFlag(&cli.IntFlag{
    Name:  "ha-connections",
    Value: 4,  // 기본 4개
})
```

## 결정

**drpc는 기본 2개의 HA 연결을 LB를 통해 서로 다른 drps 노드에 수립한다.**

### 동작 방식

```
drpc 시작
  │
  ├─ LB:7000에 TLS 연결 #1 → LB가 drps-A로 라우팅
  │   └─ yamux session + Login + NewProxy
  │
  ├─ 연결 #1 성공 후 1초 대기
  │
  ├─ LB:7000에 TLS 연결 #2 → LB가 drps-B로 라우팅
  │   └─ yamux session + Login + NewProxy
  │
  │   === 정상 운영 (2개 활성) ===
  │
  ├─ 연결 #1 끊김 감지
  │   ├─ 연결 #2로 서비스 계속 (무중단)
  │   └─ 연결 #1 재연결 시도 (exponential backoff)
  │
  │   === 복구 (2개 활성) ===
```

### LB와의 연동

drpc는 **LB 주소 하나만** 알면 된다:

```bash
drp client \
    --server lb.example.com:7000 \
    --ha-connections 2
```

LB가 라운드로빈으로 각 연결을 다른 drps에 분배:

```
drpc ──conn1──→ LB:7000 ──→ drps-A
     ──conn2──→ LB:7000 ──→ drps-B
```

### 서버 대수별 커버리지

| 서버 | HA 연결 | 직접 처리 | relay |
|------|---------|----------|-------|
| 2대 | 2 | **100%** | 0% |
| 3대 | 2 | **~67%** | ~33% |
| 5대 | 2 | **~40%** | ~60% |
| 5대 | 3 | **~60%** | ~40% |

서버 2~3대 규모에서는 HA=2로 대부분의 트래픽을 직접 처리. relay는 fallback으로만 동작.

### Supervisor 패턴 (Cloudflare 참고)

```go
type Supervisor struct {
    haConnections int
    tunnels       []*Tunnel
    tunnelErrors  chan tunnelError
}

func (s *Supervisor) Run(ctx context.Context) {
    // 1. 첫 번째 연결 수립 (성공할 때까지 재시도)
    s.startFirstTunnel(ctx)

    // 2. 나머지 HA 연결 수립 (1초 간격)
    for i := 1; i < s.haConnections; i++ {
        go s.startTunnel(ctx, i)
        time.Sleep(1 * time.Second)
    }

    // 3. 장애 감지 + 재연결 루프
    for {
        select {
        case err := <-s.tunnelErrors:
            // 끊긴 연결을 backoff로 재연결
            go s.reconnectWithBackoff(ctx, err.index)
        }
    }
}
```

### Relay는 Fallback

HA 연결로 대부분 직접 처리되지만, 커버 못하는 서버로 요청이 갈 경우 relay 사용:

```go
func (s *Server) getWorkConn(route *RouteInfo) (net.Conn, error) {
    // 1. 로컬에 drpc가 있으면 직접 처리
    if conn, err := s.localPool.Get(route.Alias); err == nil {
        return conn, nil
    }
    // 2. 없으면 relay (fallback)
    return s.cluster.RequestWorkConn(route.Node, route.Alias)
}
```

## 결과

### 장점
- **HA** — 한 서버 장애 시에도 무중단 서비스
- **WebSocket 병목 해소** — 대부분의 요청을 직접 처리. relay 최소화
- **단순한 drpc 설정** — LB 주소 하나 + `--ha-connections N`
- **서버 추가/제거 투명** — drpc 재설정 불필요 (LB가 처리)
- **검증된 패턴** — Cloudflare Tunnel이 프로덕션에서 사용

### 단점
- drpc가 여러 yamux session 유지 (메모리 수 KB. 무시 가능)
- LB 라운드로빈이 같은 서버에 두 연결을 보낼 수 있음 (확률적)
- Supervisor 코드 추가 (~50줄)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| Sticky LB (L7) | LB가 라우팅 테이블을 알아야 함. LB-Redis 연동 복잡도 |
| drpc가 모든 서버에 연결 | 서버 수에 비례하여 연결 증가. 서버 목록 관리 필요 |
| relay only (현재 frp 방식) | WebSocket 병목. 단일 장애점 |

## 참고 자료
- [cloudflare/cloudflared — supervisor.go](https://github.com/cloudflare/cloudflared/blob/master/supervisor/supervisor.go)
- [Cloudflare Tunnel HA docs](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/configure-tunnels/tunnel-availability/)
- [ADR-001](./001-minimalist-architecture.md)
- [ADR-002](./002-tls-transport.md)
