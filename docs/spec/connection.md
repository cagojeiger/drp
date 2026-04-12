# 연결 스펙

## TCP + yamux

```mermaid
flowchart LR
    frpc[frpc] -->|TCP :frpcAddr| drps[drps]
    drps --> ys[yamux.Server<br/>cfg tunable]
    ys --> s1[stream #1<br/>제어 채널]
    ys --> s2[stream #2<br/>워크 커넥션]
    ys --> s3[stream #3<br/>워크 커넥션]
    ys --> sN[stream #N<br/>...]
```

하나의 TCP 연결 위에 N개의 논리 스트림. yamux 파라미터는 `DRPS_YAMUX_*`로 튜닝 가능.

## 연결 생명주기

```mermaid
stateDiagram-v2
    [*] --> TCPConnect
    TCPConnect --> YamuxSession: yamux.Server
    YamuxSession --> AcceptStream
    AcceptStream --> FirstMsg: 스트림마다
    FirstMsg --> LoginFlow: Login
    FirstMsg --> WorkConnFlow: NewWorkConn
    FirstMsg --> [*]: timeout / unknown

    state LoginFlow {
        [*] --> AuthCheck
        AuthCheck --> Fail: invalid
        AuthCheck --> SendLoginResp: ok
        SendLoginResp --> AESWrap
        AESWrap --> SendReqWorkConn: × poolCount
        SendReqWorkConn --> ControlLoop
        ControlLoop --> HandleMsg
        HandleMsg --> ControlLoop
        ControlLoop --> Cleanup: disconnect
        Fail --> [*]
        Cleanup --> [*]
    }

    state WorkConnFlow {
        [*] --> Callback: OnWorkConn
        Callback --> PoolPut
        PoolPut --> [*]
    }
```

## 1. Login (스트림 #1)

```mermaid
sequenceDiagram
    participant F as frpc
    participant D as drps
    Note over F,D: 평문
    F->>D: Login{version, user, privilege_key, timestamp, run_id, pool_count}
    D->>D: auth.VerifyAuth
    D-->>F: LoginResp{version, run_id, error:""}
    Note over F,D: 이후 AES 래핑 시작 (DeriveKey(token))
    D-->>F: ReqWorkConn × poolCount (암호화)
```

**규칙**:
- Login 자체는 평문
- LoginResp까지 평문 → 이후부터 암호화
- `run_id`가 빈 문자열이면 drps가 새로 생성
- `HeartbeatTimeout`은 `Handler` 설정 시에만 동작 (현재 `cmd/drps/main.go` 기본값은 미설정)

## 2. 프록시 등록 (암호화)

```mermaid
sequenceDiagram
    participant F as frpc
    participant D as drps
    F->>D: NewProxy{name, type:"http", custom_domains, ...}
    alt type != "http"
        D-->>F: NewProxyResp{error: "only http"}
    else 도메인 중복
        D-->>F: NewProxyResp{error: "duplicate"}
    else
        D->>D: Router.Add
        D-->>F: NewProxyResp{name, remote_addr:":80"}
    end
```

## 3. 워크 커넥션 (스트림 #2+)

```mermaid
sequenceDiagram
    participant F as frpc
    participant D as drps
    Note over F,D: 평문
    F->>D: 새 yamux 스트림
    F->>D: NewWorkConn{run_id}
    D->>D: OnWorkConn callback
    D->>D: Pool.Put(conn)
    Note over D: handleConnection 종료<br/>(소유권 이전)
```

## 4. Heartbeat (암호화)

```mermaid
sequenceDiagram
    participant F as frpc
    participant D as drps
    loop 주기적
        F->>D: Ping
        D->>D: heartbeat 갱신
        D-->>F: Pong
    end
    Note over D: HeartbeatTimeout 초과 시<br/>conn.Close (좀비 방지)
```

## 재연결 (같은 RunID)

```mermaid
sequenceDiagram
    participant F1 as frpc (old, dead)
    participant D as drps
    participant F2 as frpc (new)

    F1--xD: 네트워크 끊김
    F2->>D: Login{run_id: "abc"}
    D->>D: controlManager.GetEntry("abc")
    D->>D: old cancel() → old ctx Done
    D->>D: old controlLoop 종료
    D->>D: Router.Remove(old proxies)
    D->>D: Registry.Remove(old pool)
    D->>D: controlManager.Register(new)
    D-->>F2: LoginResp{ok}
    F2->>D: NewProxy 재등록
```

현재 구현은 `Register()`에서 old `cancel()` 후 new 엔트리를 즉시 등록한다.
old 세션 정리는 비동기로 이어서 진행되며, cleanup 완료를 동기적으로 기다리지는 않는다.

## 연결 해제

```mermaid
flowchart TB
    disc[frpc 끊김] --> err[ReadMsg error]
    err --> exit[controlLoop 종료]
    exit --> rm1[Router.Remove<br/>해당 frpc의 모든 route]
    exit --> cb[OnControlClose]
    cb --> rm2[Registry.Remove<br/>풀 닫기]
    exit --> rm3[controlManager.Remove]
```

구현: `internal/server/handle.go` 는 refactor rounds 1-3 에서 5개 파일로 분할되었다. 기존 `metrics.go`, `util.go` 는 그대로 유지된다.

| 파일 | 담당 |
|---|---|
| `handle.go` | `Handler`, `HandleConnection`, `handleLogin`, `controlLoop`, `bootstrapReqWorkConn`, `cleanupControlSession` |
| `control_writer.go` | `controlWriter` + `sendLoop` wrapper, adaptive batching, flush timer |
| `control_manager.go` | `controlManager`, `controlEntry` (per-session state, RLock 핫패스) |
| `proxy_register.go` | `handleNewProxy` (NewProxy → router 등록 + rollback) |
| `stats.go` | `ReqWorkConnStats`, `ReqWorkConnSnapshot` |
| `metrics.go` | `MetricsHandler` — `/__drps/metrics` JSON 엔드포인트 (preexisting) |
| `util.go` | `generateRunID` — 랜덤 8-byte hex (preexisting) |

세션 teardown 은 `cancel()` 단일 시그널로만 전파된다 — `reqCh`/`sendCh` 는 **never closed**. `cleanupControlSession` 은 `cancel()` → `controls.Remove` → `Router.Remove(registeredProxies)` → `OnControlClose(runID)` 콜백 순으로 정리하지만, 채널 close 는 없다.
