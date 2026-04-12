# drps 아키텍처 개요

frps(frp server)의 HTTP 전용 대체. frpc를 수정 없이 사용하며, frp 패키지를 import하지 않고 프로토콜을 직접 구현한다.

## 전체 구성

```mermaid
flowchart LR
    Client[클라이언트<br/>HTTP/WS]
    subgraph drps["drps 서버"]
        direction TB
        HTTP[HTTP Listener<br/>:18080]
        Proxy[proxy<br/>Handler]
        Router[router<br/>Table]
        Registry[pool<br/>Registry]
        Wrap[wrap<br/>Encrypt/Compress]
        TCP[TCP Listener<br/>:17000]
        Server[server<br/>Handler]
        Metrics[/metrics/<br/>Handler]
    end
    Frpc[frpc<br/>클라이언트]
    Backend[백엔드<br/>HTTP/WS]

    Client -->|요청| HTTP
    HTTP --> Proxy
    Proxy --> Router
    Router --> Registry
    Registry --> Wrap
    Wrap -->|yamux stream| Frpc
    Frpc --> Backend

    Frpc -->|TCP yamux| TCP
    TCP --> Server
    Server -.-> Router
    Server -.-> Registry

    HTTP -.-> Metrics
    Metrics -.-> Server
    Metrics -.-> Registry
```

## 데이터 흐름

HTTP 요청과 제어 채널은 완전히 분리된다.

```mermaid
flowchart TB
    subgraph control["제어 채널 (TCP :17000)"]
        direction LR
        C1[frpc Login] --> C2[LoginResp]
        C2 --> C3[ReqWorkConn N회<br/>poolCount 만큼]
        C3 --> C4[NewProxy]
        C4 --> C5[NewProxyResp]
        C5 --> C6[Ping/Pong 주기적]
    end

    subgraph work["워크 커넥션 (yamux stream)"]
        direction LR
        W1[frpc NewWorkConn] --> W2[Pool.Put]
        W2 --> W3[HTTP 요청 도착]
        W3 --> W4[Pool.Get]
        W4 --> W5[wrap 암호화/압축]
        W5 --> W6[HTTP 요청 전달]
        W6 --> W7[응답 수신]
    end

    control -.-> work
```

## HTTP mux 구성

```mermaid
flowchart TB
    mux[http.ServeMux]
    root[/]
    metrics[/__drps/metrics]
    pprof[/debug/pprof/*]

    mux --> root
    mux --> metrics
    mux --> pprof

    root --> proxy[proxy.Handler<br/>h2c ReverseProxy]
    metrics --> mh[server.MetricsHandler]
    pprof --> ph[net/http/pprof]
```

- `DRPS_PPROF=1`일 때만 pprof 라우트가 등록된다.
- `DRPS_DEBUG=1`일 때 제어채널/워크커넥션 로그를 추가 출력한다.

## 패키지 구조

```mermaid
flowchart TB
    main[cmd/drps/main.go<br/>엔트리포인트<br/><br/>buildServerStack<br/>buildHTTPMux<br/>runFrpcAccept]

    subgraph cfg["설정"]
        config[internal/config]
    end

    subgraph proto["프로토콜 계층"]
        server[internal/server<br/><br/>handle.go<br/>control_writer.go<br/>control_manager.go<br/>proxy_register.go<br/>stats.go]
        msg[internal/msg]
        auth[internal/auth]
        crypto[internal/crypto]
    end

    subgraph svc["서비스 계층"]
        proxy[internal/proxy<br/><br/>newReverseProxy<br/>newTransport<br/>routeDialKey<br/>routeFromCtx]
        router[internal/router]
        pool[internal/pool<br/><br/>connection queue<br/>refill machinery<br/>statistics<br/>teardown]
        wrap[internal/wrap]
    end

    main --> config
    main --> server
    main --> proxy
    main --> router
    main --> pool

    server --> msg
    server --> auth
    server --> crypto
    server --> router

    proxy --> router
    proxy --> pool
    proxy --> wrap

    wrap --> msg
    wrap --> crypto
```

## 의존성 방향

순환 의존성 없음. 각 패키지는 단일 책임을 갖는다.

```mermaid
flowchart LR
    main[main.go]
    config[config]
    server[server]
    proxy[proxy]
    router[router]
    pool[pool]
    wrap[wrap]
    msg[msg]
    auth[auth]
    crypto[crypto]

    main --> config
    main --> server
    main --> proxy
    main --> router
    main --> pool

    server --> msg
    server --> auth
    server --> crypto
    server --> router

    proxy --> router
    proxy --> pool
    proxy --> wrap

    wrap --> msg
    wrap --> crypto

    classDef indep fill:#e8f5e9,stroke:#2e7d32
    classDef compose fill:#e3f2fd,stroke:#1565c0
    class config,pool,router,msg,auth,crypto indep
    class main,server,proxy,wrap compose
```

초록 = 독립 패키지, 파랑 = 합성 패키지.

## 성능 설계 원칙

```mermaid
mindmap
  root((성능 설계))
    Hot Path 할당 최소화
      sync.Pool
      ReverseProxy BufferPool 재사용
      msg 버퍼 재사용
    중복 계산 제거
      HTTP 워크경로 PBKDF2 키 캐시
      Router 직접 조회
    syscall 최소화
      WriteMsg 단일 Write
      배치 전송 sendLoop
    리플렉션 제거
      switch type assertion
      fmt.Sprintf 제거
    동시성 최적화
      RWMutex for reads
      atomic 통계
      워커 goroutine 분리
```

## 관련 문서

- [components.md](components.md) — 각 컴포넌트 상세 구조
- [flows.md](flows.md) — 주요 시나리오 시퀀스 다이어그램
