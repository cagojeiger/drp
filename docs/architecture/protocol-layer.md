# Protocol Layer

frpc 클라이언트와의 프로토콜 통신을 담당합니다.
이 레이어의 관심사는 **"frpc와 어떻게 대화할까?"** 입니다.

## 연결 구조: yamux

하나의 TCP 연결 위에 여러 논리 스트림을 만듭니다.

```mermaid
graph TB
    frpc[frpc Client]

    subgraph yamux-Session
        S0[Stream-0: 제어 채널<br/>AES-128-CFB 암호화]
        S1[Stream-1: WorkConn]
        S2[Stream-2: WorkConn]
        SN[Stream-N: WorkConn]
    end

    frpc -->|TCP 1개| yamux-Session
    S0 --> Control
    S1 --> BridgeLayer
    S2 --> BridgeLayer
    SN --> BridgeLayer
```

설정: `MaxStreamWindowSize = 6MB`, `KeepAliveInterval = 30s`

## Login 흐름

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer

    F->>P: Login (평문)
    Note right of F: version, run_id,<br/>privilege_key: MD5(token+ts),<br/>pool_count

    P->>P: MD5(token+timestamp) 검증
    P-->>F: LoginResp (평문)
    Note left of P: version, run_id, error

    P->>P: CryptoReadWriter 설정
    Note over P: 이후 모든 제어 메시지<br/>AES-128-CFB 암호화<br/>key=token, salt=frp
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

    Token -->|"MD5(token+ts)"| Auth[1.인증<br/>privilege_key]
    Token -->|"AES key=token<br/>salt=frp"| Ctrl[2.제어-채널<br/>Login-이후]
    Token -->|"AES key=token<br/>salt=frp"| Work[3.워크-커넥션<br/>useEncryption=true]
```

## 프록시 등록 흐름

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer
    participant B as BridgeLayer

    F->>P: NewProxy (AES 암호화)
    Note right of F: type:http, domains,<br/>locations, headers,<br/>useEncryption

    P->>P: proxy_type 검증 (http만)
    P->>P: subdomain 검증
    P->>P: 도메인 목록 구성
    P->>P: RouteConfig 생성
    P->>B: ProxyRegistrar.Register(RouteConfig)
    B-->>P: OK / conflict error
    P-->>F: NewProxyResp
```

## 워크 커넥션 풀

```mermaid
sequenceDiagram
    participant S as ServiceLayer
    participant B as BridgeLayer
    participant P as ProtocolLayer
    participant F as frpc

    Note over P,F: 풀 초기화 (Login 직후)
    P-->>F: ReqWorkConn (pool_count만큼)
    F->>P: NewWorkConn (새 yamux 스트림)
    P->>P: workConnCh에 저장

    Note over S: HTTP 요청 도착
    S->>B: GetWorkConn()
    B->>P: Control.GetWorkConn()

    alt 풀에-있음
        P-->>B: conn (즉시)
        P-->>F: ReqWorkConn (보충)
    else 풀-비어있음
        P-->>F: ReqWorkConn
        F->>P: NewWorkConn
        P-->>B: conn (대기 후)
        P-->>F: ReqWorkConn (보충)
    end
```

## 재연결

```mermaid
sequenceDiagram
    participant F as frpc
    participant P as ProtocolLayer
    participant B as BridgeLayer

    Note over F: 연결 끊김 후 재연결

    F->>P: Login (같은 run_id)
    P->>P: ControlManager에서 기존 Control 발견
    P->>B: 기존 라우트 해제
    P->>P: 기존 워크 커넥션 정리
    P->>P: 새 Control 생성 + 포인터 교체

    F->>P: NewProxy (재등록)
    P->>B: Register(RouteConfig)
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
