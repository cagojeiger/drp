# Protocol Layer

frpc 클라이언트와의 프로토콜 통신을 담당합니다.
이 레이어의 관심사는 **"frpc와 어떻게 대화할까?"** 입니다.

## 연결 구조: yamux

하나의 TCP 연결 위에 여러 논리 스트림을 만듭니다.

```mermaid
graph TB
    frpc[frpc Client]

    subgraph YamuxSession["yamux Session"]
        S0["Stream 0: 제어 채널 (AES)"]
        S1["Stream 1: WorkConn"]
        S2["Stream 2: WorkConn"]
        SN["Stream N: WorkConn"]
    end

    frpc -->|TCP| YamuxSession
    S0 --> Ctrl[Control]
    S1 --> BL[Bridge Layer]
    S2 --> BL
    SN --> BL
```

설정: `MaxStreamWindowSize = 6MB`, `KeepAliveInterval = 30s`

## Login 흐름

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer

    F->>P: Login (plaintext)
    Note right of F: version, run_id,<br/>privilege_key MD5,<br/>pool_count

    P->>P: verify MD5
    P-->>F: LoginResp (plaintext)
    Note left of P: version, run_id, error

    P->>P: setup CryptoReadWriter
    Note over P: AES-128-CFB<br/>key=token salt=frp
```

**중요:** Login 메시지 자체는 **평문**. Login 성공 후부터 암호화.

## 메시지 프로토콜

### 프레임 형식

```
[1 byte: Type] [8 bytes: Length (big-endian)] [N bytes: JSON Body]
```

### 메시지 타입

| 바이트 | 이름 | 방향 | 설명 |
|--------|------|------|------|
| `'o'` | Login | frpc→drps | 로그인 요청 |
| `'1'` | LoginResp | drps→frpc | 로그인 응답 |
| `'p'` | NewProxy | frpc→drps | 프록시 등록 |
| `'2'` | NewProxyResp | drps→frpc | 등록 응답 |
| `'c'` | CloseProxy | frpc→drps | 프록시 해제 |
| `'r'` | ReqWorkConn | drps→frpc | 워크 커넥션 요청 |
| `'w'` | NewWorkConn | frpc→drps | 워크 커넥션 등록 |
| `'s'` | StartWorkConn | drps→frpc | 워크 커넥션 사용 시작 |
| `'h'` | Ping | frpc→drps | 하트비트 |
| `'4'` | Pong | drps→frpc | 하트비트 응답 |

## 암호화 구조

`auth.token` 하나로 세 가지를 처리합니다:

```mermaid
graph LR
    Token[auth.token]

    Token -->|MD5| Auth["1. 인증 privilege_key"]
    Token -->|AES| Ctrl2["2. 제어 채널 (Login 이후)"]
    Token -->|AES| Work["3. 워크 커넥션 (useEncryption)"]
```

## 프록시 등록 흐름

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer
    participant B as BridgeLayer

    F->>P: NewProxy (AES encrypted)
    Note right of F: type http, domains,<br/>locations, headers,<br/>useEncryption

    P->>P: validate proxy_type
    P->>P: validate subdomain
    P->>P: build domain list
    P->>P: create RouteConfig
    P->>B: Register RouteConfig
    B-->>P: OK or conflict
    P-->>F: NewProxyResp
```

## 워크 커넥션 풀

```mermaid
sequenceDiagram
    participant S as ServiceLayer
    participant B as BridgeLayer
    participant P as ProtocolLayer
    participant F as frpc

    Note over P,F: Pool init after Login
    P-->>F: ReqWorkConn x pool_count
    F->>P: NewWorkConn (new yamux stream)
    P->>P: store in workConnCh

    Note over S: HTTP request arrives
    S->>B: GetWorkConn()
    B->>P: Control.GetWorkConn()

    alt PoolHit
        P-->>B: conn immediate
        P-->>F: ReqWorkConn refill
    else PoolMiss
        P-->>F: ReqWorkConn
        F->>P: NewWorkConn
        P-->>B: conn after wait
        P-->>F: ReqWorkConn refill
    end
```

## 재연결

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer
    participant B as BridgeLayer

    Note over F: reconnect after disconnect

    F->>P: Login same run_id
    P->>P: find existing Control
    P->>B: Unregister old routes
    P->>P: close old WorkConns
    P->>P: create new Control

    F->>P: NewProxy re-register
    P->>B: Register RouteConfig
```

## Bridge Layer와의 인터페이스

Protocol Layer는 `ProxyRegistrar` 인터페이스를 통해 Bridge Layer에 라우트를 등록합니다.
Router의 구체적인 구현을 알지 못합니다.

```go
type ProxyRegistrar interface {
    Register(rc *RouteConfig) error
    Unregister(proxyName string)
}
```

## 소스 파일

| 파일 | 역할 |
|------|------|
| `service.go` (TCP 부분) | TCP 리스너, yamux 세션, 메시지 분기 |
| `control.go` | 제어 채널 암호화, 디스패처, 워크 커넥션 풀, 프록시 등록 |
| `control_manager.go` | run_id → Control 맵, 재연결 가드 |
| `auth.go` | MD5 토큰 인증 |
