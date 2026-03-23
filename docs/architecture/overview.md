# drps 아키텍처 개요

## drps란?

drps는 frp의 서버(frps)를 HTTP 전용으로 새로 만든 리버스 프록시 서버입니다.
frps는 단일 인스턴스로만 동작하지만, drps는 분산 환경을 목표로 설계합니다.

## 3개 레이어

drps는 **두 개의 서로 다른 프로토콜을 연결하는 브릿지**입니다.

```mermaid
graph TB
    Client[HTTP Client]
    Ingress[Ingress/TLS]
    frpc[frpc Client]
    Local[localhost:3000]

    Client -->|HTTP/1.1| Ingress
    Ingress -->|HTTP| SL

    subgraph drps
        SL[Service Layer]
        BL[Bridge Layer]
        PL[Protocol Layer]

        SL -->|RouteConfig| BL
        BL -->|WorkConn| PL
    end

    PL -->|yamux + AES| frpc
    frpc --> Local
```

| 레이어 | 관심사 | 상세 |
|--------|--------|------|
| **Service Layer** | "이 HTTP 요청을 어떻게 처리할까?" | [service-layer.md](service-layer.md) |
| **Bridge Layer** | "이 요청을 어떤 frpc에게 보낼까?" | [bridge-layer.md](bridge-layer.md) |
| **Protocol Layer** | "frpc와 어떻게 대화할까?" | [protocol-layer.md](protocol-layer.md) |

## 전체 요청 흐름

```mermaid
sequenceDiagram
    participant C as HTTP Client
    participant S as ServiceLayer
    participant B as BridgeLayer
    participant P as ProtocolLayer
    participant F as frpc

    C->>S: GET / Host app.example.com
    S->>B: Lookup domain path
    B-->>S: RouteConfig
    S->>S: BasicAuth
    S->>B: GetWorkConn()
    B->>P: Control.GetWorkConn()
    P-->>B: yamux stream
    B->>B: AES snappy wrap
    B-->>S: net.Conn
    S->>F: HTTP 요청 전달
    F-->>S: HTTP 응답
    S->>S: ModifyResponse
    S-->>C: HTTP Response
```

## frpc 등록 흐름

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer
    participant B as BridgeLayer

    F->>P: TCP connect yamux
    F->>P: Login plaintext
    P->>P: verify auth
    P-->>F: LoginResp
    P->>P: start AES encryption

    F->>P: NewProxy encrypted
    Note right of P: type http domain location<br/>headers auth encryption
    P->>B: Register RouteConfig
    B->>B: add to routing table
    P-->>F: NewProxyResp

    P-->>F: ReqWorkConn pool init
    F->>P: NewWorkConn new stream
    P->>P: store in workConnCh
```

## 레이어 독립성

```mermaid
graph LR
    subgraph ServiceLayer
        HTTP[HTTP 리스너]
        RP[ReverseProxy]
        Auth[Basic Auth]
    end

    subgraph BridgeLayer
        Router[라우팅 테이블]
        RC[RouteConfig]
        IF[인터페이스]
    end

    subgraph ProtocolLayer
        TCP[TCP 리스너]
        Ctrl[Control]
        Pool[WorkConn Pool]
    end

    RP -->|Lookup| Router
    Ctrl -->|Register| IF
    IF -.->|impl| Router
    RP -->|GetWorkConn| RC
    RC -->|call| Pool
```

각 레이어는 인터페이스를 통해서만 소통합니다.
**분산 확장 시 Bridge Layer만 교체하면 됩니다.**

## 파일 매핑

| 레이어 | 소스 파일 | 역할 |
|--------|----------|------|
| Protocol | `service.go` (TCP) | frpc 연결 수락, yamux |
| Protocol | `control.go` | 제어 채널, 메시지 처리, 워크 커넥션 풀 |
| Protocol | `control_manager.go` | 모든 frpc 관리 |
| Protocol | `auth.go` | 토큰 인증 |
| Service | `service.go` (HTTP) | HTTP 요청 수신 |
| Service | `httpproxy.go` | ReverseProxy, 헤더, WebSocket |
| Bridge | `router.go` | 라우팅 테이블 |
| Bridge | `interfaces.go` | 레이어 간 계약 |
| Bridge | `config.go` | 서버 설정 |
