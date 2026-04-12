# 주요 시나리오 플로우

drps의 핵심 동작을 시퀀스 다이어그램으로 설명한다.

## 1. frpc 연결 수립 (Login)

```mermaid
sequenceDiagram
    participant F as frpc
    participant T as TCP Listener
    participant H as server.Handler
    participant A as auth
    participant R as router
    participant CM as controlManager

    F->>T: TCP connect :17000
    T->>H: HandleConnection(conn)
    F->>H: Login (token, RunID, PoolCount)
    H->>A: VerifyAuth(token, privilegeKey)
    A-->>H: ok
    H->>H: handleLogin()
    H-->>F: LoginResp(RunID, Error="")
    H->>H: AES wrap conn (DeriveKey(token))
    H->>CM: Register(runID, cancel, reqCh, sendCh)
    H->>H: goroutine: sendLoop(w, reqCh, sendCh, stats)
    H->>H: goroutine: bootstrapReqWorkConn(PoolCount)

    loop poolCount 회
        H->>F: ReqWorkConn (via sendLoop)
    end

    H->>H: controlLoop(conn) 진입
```

## 2. NewProxy 등록

```mermaid
sequenceDiagram
    participant F as frpc
    participant H as server.controlLoop
    participant R as router
    participant RP as registeredProxies

    F->>H: NewProxy (ProxyName, CustomDomains, Locations, ...)
    alt type != http
        H-->>F: NewProxyResp(Error="only http")
    else
        H->>R: Add(RouteConfig{Domain, Location, ProxyName, RunID, ...})
        alt 중복 도메인
            R-->>H: error
            H-->>F: NewProxyResp(Error="duplicate")
        else
            R-->>H: ok
            H->>RP: add proxyName
            H-->>F: NewProxyResp(Error="")
        end
    end
```

## 3. HTTP 요청 프록싱

```mermaid
sequenceDiagram
    participant C as Client
    participant P as proxy.Handler
    participant R as router
    participant Reg as pool.Registry
    participant Pl as Pool
    participant W as wrap
    participant F as frpc
    participant B as Backend

    C->>P: HTTP request (Host: app.example.com)
    P->>R: Lookup(host, path)
    R-->>P: RouteConfig{RunID, ...}

    opt HTTPUser 설정됨
        P->>P: Basic Auth 검증
    end

    P->>Reg: Get(runID)
    Reg-->>P: *Pool
    P->>Pl: Get(timeout)

    alt pool 비어있음
        Pl->>Pl: requestAsyncRefill(burst)
        Pl-->>P: 대기...
        Note right of Pl: sendLoop → ReqWorkConn → frpc
        F->>Pl: NewWorkConn (새 stream)
        Pl->>Pl: Put(conn)
    end
    Pl-->>P: conn

    P->>W: Wrap(conn, aesKey, proxyName, enc, comp)
    W->>F: StartWorkConn(ProxyName)
    W-->>P: wrapped conn

    P->>P: ReverseProxy Rewrite (HostHeaderRewrite + custom headers)
    P->>F: HTTP request (via wrapped stream / DialContext)
    F->>B: HTTP request
    B-->>F: HTTP response
    F-->>P: HTTP response
    P-->>C: HTTP response

    Note right of P: 101 업그레이드 포함\nReverseProxy가 터널링 처리

    P->>Pl: eager refill (async)
```

## 4. 워크 커넥션 수명주기

```mermaid
sequenceDiagram
    participant P as proxy.Handler
    participant Pl as Pool
    participant RW as refillWorker
    participant SL as sendLoop
    participant F as frpc
    participant S as server.controlLoop

    Note over Pl,RW: Pool 생성 시 refillWorker 시작

    P->>Pl: Get(timeout)
    alt 커넥션 있음 (hit)
        Pl-->>P: conn
        Pl->>RW: async refill (+1)
    else 비어있음 (miss)
        Pl->>Pl: missRefillBurst() → N개
        Pl->>RW: async refill (+N)
        Pl-->>P: 대기...
    end

    RW->>RW: pending += N
    RW->>SL: reqCh <- {} (N회)

    loop N회
        SL->>SL: batch wait (adaptive flush)
        SL->>F: ReqWorkConn (AES 암호화)
    end

    F->>S: new yamux stream
    S->>S: HandleConnection → NewWorkConn
    S->>Pl: Put(conn)

    alt Pool 대기자 있음
        Pl-->>P: conn
    else
        Pl->>Pl: 채널에 보관
    end
```

## 5. 재연결 처리

```mermaid
sequenceDiagram
    participant F1 as frpc(old)
    participant H as server.Handler
    participant CM as controlManager
    participant R as router
    participant F2 as frpc(new)

    F1->>H: Login (RunID=abc)
    H->>CM: Register(abc, cancel1, ...)

    Note over F1,H: 네트워크 끊김 or crash

    F2->>H: Login (RunID=abc)
    H->>CM: lookup abc
    CM-->>H: existing entry (cancel1)
    H->>CM: cancel1() → old controlLoop 종료
    H->>R: Remove(all proxies of old session)
    H->>CM: Register(abc, cancel2, ...)
    H-->>F2: LoginResp(ok)
```

## 6. 메트릭 엔드포인트

```mermaid
sequenceDiagram
    participant C as Client
    participant HTTP as HTTP Listener
    participant M as MetricsHandler
    participant RS as ReqWorkConnStats
    participant Reg as pool.Registry

    C->>HTTP: GET /__drps/metrics
    HTTP->>M: ServeHTTP
    M->>RS: Snapshot() (atomic loads)
    RS-->>M: {requested, enqueued, dropped, sent, inflight}
    M->>Reg: AggregateStats() (모든 pool 순회)
    Reg-->>M: {get_hit, get_miss, refill_demand, refill_sent, active_pools}
    M->>M: JSON 인코딩
    M-->>C: 200 OK + JSON
```

## 7. sendLoop 배칭 동작

제어 채널은 **단일 writer** 원칙. `sendLoop`가 유일한 writer.

```mermaid
flowchart TB
    reqCh[(reqCh<br/>ReqWorkConn 요청)]
    sendCh[(sendCh<br/>일반 메시지)]
    SL[sendLoop]
    stats[ReqWorkConnStats]
    writer[AES writer]

    refill[Pool refillWorker] -->|signal| reqCh
    control[controlLoop<br/>NewProxyResp, Pong] -->|msg| sendCh

    reqCh --> SL
    sendCh --> SL
    SL -->|batch+flush| writer
    SL -.->|atomic| stats

    writer -->|yamux stream| frpc[frpc]

    classDef chan fill:#fff3e0,stroke:#e65100
    class reqCh,sendCh chan
```

배칭 규칙:
- 큐가 깊으면 flush 간격 짧게 (floor 50μs)
- 기본 200μs, 최대 400μs
- 즉시 flush 조건: sendCh (일반 메시지)가 들어오면 우선 전송

## 8. 서버 시작 시 초기화

`main()` 은 얇은 orchestrator 이고, setup 은 네 개의 named helper 로 분해되어있다: `buildServerStack`, `buildHTTPMux`, `runFrpcAccept`, `handleFrpcConnection` (+ `openYamuxSession`).

```mermaid
sequenceDiagram
    participant M as main()
    participant BSS as buildServerStack
    participant BHM as buildHTTPMux
    participant RFA as runFrpcAccept
    participant HTTP as srv.ListenAndServe

    M->>M: config.Load()
    M->>BSS: buildServerStack(cfg, debug)
    BSS->>BSS: router.New()
    BSS->>BSS: pool.NewRegistry()
    BSS->>BSS: crypto.DeriveKey(token) 1회
    BSS->>BSS: server.Handler{Router, OnWorkConn, OnControlClose}
    BSS->>BSS: proxy.NewHandler(router, registry.Get, aesKey)
    BSS-->>M: *serverStack

    M->>BHM: buildHTTPMux(stack, cfg)
    BHM->>BHM: mux.HandleFunc /__drps/metrics
    opt DRPS_PPROF=1
        BHM->>BHM: registerPprofEndpoints(mux)
    end
    BHM->>BHM: mux.Handle("/", stack.proxyHandler)
    BHM-->>M: *http.ServeMux

    M->>M: net.Listen("tcp", cfg.FrpcAddr)
    M->>RFA: go runFrpcAccept(ln, handler, cfg)
    M->>HTTP: srv.ListenAndServe() blocks
```

### frpc accept 루프

```mermaid
flowchart LR
    RFA[runFrpcAccept<br/>for ln.Accept]
    HFC[handleFrpcConnection<br/>per-conn]
    OYS[openYamuxSession<br/>yamux tuning]
    Loop[for session.AcceptStream]
    HC[h.HandleConnection<br/>per-stream]

    RFA -->|go| HFC
    HFC --> OYS
    OYS --> Loop
    Loop -->|go| HC
```

각 단계는 별도 함수로 분리되어 main() 이 "config → stack → mux → listener → go accept → serve" 의 **의도 레벨** 로만 읽힌다. yamux tuning (MaxStreamWindowSize 등) 은 `openYamuxSession` 안에만 존재.
