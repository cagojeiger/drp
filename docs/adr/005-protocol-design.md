# ADR-005: Protocol Design (TLV + JSON)

## 상태
Accepted

## 컨텍스트

drpc ↔ drps 간 제어 메시지와 drps ↔ drps 간 릴레이 메시지를 위한 프로토콜이 필요하다.

frp는 `[1 byte type][4 byte length][JSON body]` 포맷을 사용하며, 이 포맷은:
- 파싱이 단순하다 (타입 1바이트 → 디스패치, 길이 4바이트 → 정확한 읽기)
- JSON body라 디버깅이 용이하다
- 제어 메시지는 빈도가 낮아 JSON 오버헤드가 무시할 수 있다

## 결정

**TLV(Type-Length-Value) + JSON 메시지 포맷을 사용한다.**

### 메시지 포맷

```
┌──────────┬──────────────┬──────────────────┐
│ Type     │ Length       │ Body             │
│ 1 byte   │ 4 bytes (BE) │ JSON (variable)  │
└──────────┴──────────────┴──────────────────┘
```

### 메시지 타입

```
제어 채널 (drpc ↔ drps):

  'L' Login            drpc → drps   인증 요청
  'l' LoginResp        drps → drpc   인증 응답 + 서버 목록
  'P' NewProxy         drpc → drps   프록시 등록
  'p' NewProxyResp     drps → drpc   등록 결과
  'R' ReqWorkConn      drps → drpc   work conn 요청
  'W' NewWorkConn      drpc → drps   work conn 제공
  'S' StartWorkConn    drps → drpc   relay 시작
  'H' Ping             drpc → drps   heartbeat
  'h' Pong             drps → drpc   heartbeat 응답

노드 간 (drps ↔ drps, fallback relay):

  'Q' RelayReq         drps → drps   원격 work conn 요청
  'q' RelayResp        drps → drps   응답 후 TCP relay 전환
```

### 메시지 구조체

```go
// --- 인증 ---
type Login struct {
    APIKey string `json:"api_key"`
    RunID  string `json:"run_id"`
}

type LoginResp struct {
    RunID string   `json:"run_id"`
    Nodes []string `json:"nodes,omitempty"`  // HA 연결용 서버 목록
    Error string   `json:"error,omitempty"`
}

// --- 프록시 등록 ---
type NewProxy struct {
    Alias          string   `json:"alias"`
    Hostname       string   `json:"hostname"`
    LocalAddr      string   `json:"local_addr"`
    AllowedIPs     []string `json:"allowed_ips,omitempty"`
    MaxConnections int      `json:"max_connections,omitempty"`
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

// --- 노드 간 Relay (fallback) ---
type RelayReq struct {
    Alias string `json:"alias"`
}

type RelayResp struct {
    Error string `json:"error,omitempty"`
}
```

### HA 연결 흐름

```
drpc                                          drps-A (via LB)
 │                                             │
 ├── [TLS connect to LB:7000] ───────────────→│
 │                                             │
 │   === yamux session 수립 ===                 │
 │                                             │
 ├── Login{APIKey, RunID:""}  ────────────────→│ API Key 검증 + RunID 생성
 │← LoginResp{RunID, Nodes:["...:7000"x3]} ──│ ← 서버 목록 전달
 │                                             │
 ├── NewProxy{Alias, Hostname, ...} ─────────→│ Redis에 등록
 │← NewProxyResp{Alias, URL}  ───────────────│
 │                                             │
 │   drpc: Nodes에서 추가 서버 선택             │
 │         LB:7000으로 2번째 연결 시도          │
 │                                             │
 ├── [TLS connect to LB:7000] ───────────────→│ drps-B (LB가 다른 서버로)
 │   Login + NewProxy 반복                      │
 │                                             │
 │   === HA 연결 2개 활성 ===                   │
```

## 결과

### 장점
- frp와 동일한 검증된 패턴
- 파싱 코드 최소화 (타입 1바이트 switch + JSON unmarshal)
- `tcpdump`로 디버깅 가능 (JSON 평문, TLS 없을 때)
- LoginResp에 Nodes 포함 → drpc가 서버 목록을 자동으로 받음

### 단점
- JSON은 protobuf 대비 파싱 비용 높음 (제어 메시지 빈도가 낮아 무시 가능)
- 스키마 검증이 런타임에만 가능

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| protobuf | 제어 메시지 빈도 낮음. 코드 생성 도구 의존. 디버깅 어려움 |
| msgpack | JSON 대비 이점이 미미. 디버깅 불편 |
| gRPC | HTTP/2 필요. 바이너리 릴레이 전환이 부자연스러움 |

## 참고 자료
- drp CONTEXT.md §4
- [ADR-001](./001-minimalist-architecture.md)
- [ADR-007](./007-ha-connections.md)
