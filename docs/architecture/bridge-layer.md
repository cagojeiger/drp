# Bridge Layer

Service Layer의 HTTP 요청을 Protocol Layer의 워크 커넥션에 연결합니다.
이 레이어의 관심사는 **"이 요청을 어떤 frpc에게 보낼까?"** 입니다.

## 브릿지 역할

```mermaid
flowchart LR
    SL[Service Layer<br/>HTTP 요청]
    BL[Bridge Layer<br/>라우팅+연결]
    PL[Protocol Layer<br/>워크 커넥션]

    SL -->|"1.Lookup(domain,path)"| BL
    BL -->|"2.GetWorkConn()"| PL
    PL -->|"3.yamux stream"| BL
    BL -->|"4.암호화 래핑"| BL
    BL -->|"5.net.Conn 반환"| SL
```

## RouteConfig: 두 레이어를 연결하는 구조체

```mermaid
classDiagram
    class RouteConfig {
        +Domain string
        +Location string
        +ProxyName string
        +GetWorkConn() net.Conn
        +HostHeaderRewrite string
        +Headers map
        +ResponseHeaders map
        +HTTPUser string
        +HTTPPwd string
        +UseEncryption bool
        +UseCompression bool
    }

    ServiceLayer ..> RouteConfig : Lookup으로 조회
    ProtocolLayer ..> RouteConfig : GetWorkConn 제공
    BridgeLayer --> RouteConfig : 생성 및 관리
```

`GetWorkConn`이 **Protocol Layer와 Service Layer를 연결하는 유일한 접점**입니다.
Service Layer는 이 함수를 호출하면 `net.Conn`을 받을 뿐,
그것이 yamux 스트림인지, 어떤 frpc에서 왔는지 알지 못합니다.

## 라우팅 테이블

```mermaid
graph TD
    Router[Router]
    Router --> D1["app.example.com"]
    Router --> D2["*.example.com"]
    Router --> D3["*"]

    D1 --> R1["/api → RouteConfig"]
    D1 --> R2["/ → RouteConfig"]
    D2 --> R3["/ → RouteConfig"]
```

location은 **내림차순 정렬** (longest-prefix match):
`/api/v2` → `/api` → `/`

### 라우팅 알고리즘

```mermaid
flowchart TD
    Start["Lookup(host,path)"] --> Exact{정확한 도메인?}
    Exact -->|있음| Match[location prefix match]
    Exact -->|없음| Wild1{"*.example.com?"}
    Wild1 -->|있음| Match
    Wild1 -->|없음| Wild2{"*.com?"}
    Wild2 -->|있음| Match
    Wild2 -->|없음| Wild3{"*?"}
    Wild3 -->|있음| Match
    Wild3 -->|없음| NotFound[nil → 404]
    Match --> Found[RouteConfig 반환]
```

## 워크 커넥션 암호화 래핑

```mermaid
flowchart TD
    WC[워크 커넥션 획득] --> SWC[StartWorkConn 메시지 전송]
    SWC --> Enc{UseEncryption?}
    Enc -->|true| AES["AES-128-CFB 래핑"]
    Enc -->|false| Comp{UseCompression?}
    AES --> Comp
    Comp -->|true| Snappy["snappy 압축 래핑"]
    Comp -->|false| Done[net.Conn 반환]
    Snappy --> Done
```

래핑 순서 (양쪽 동일해야 함):
- drps: `yamux stream → [AES] → [snappy] → HTTP I/O`
- frpc: `yamux stream → [AES] → [snappy] → localhost`

## 인터페이스 설계

```mermaid
classDiagram
    class ProxyRegistrar {
        <<interface>>
        +Register(RouteConfig) error
        +Unregister(proxyName)
    }

    class ControlRegistry {
        <<interface>>
        +Del(runID, Control)
    }

    class Router {
        +Register()
        +Unregister()
        +Lookup()
    }

    class DistributedRouter {
        +Register()
        +Unregister()
        +Lookup()
    }

    ProxyRegistrar <|.. Router : Phase1
    ProxyRegistrar <|.. DistributedRouter : Phase2
    ControlRegistry <|.. ControlManager
```

## Phase 2: 분산 확장

### 문제

```mermaid
flowchart LR
    User[사용자]
    A[drps-A]
    B[drps-B]
    F1[frpc-1]

    F1 -->|연결| A
    User -->|"app.example.com"| B
    B -.->|"frpc-1은 A에 있음!"| A
```

### 해결: 라우팅 정보에 drps 인스턴스 포함

```
Phase 1 라우팅 테이블:
  "app.example.com" → GetWorkConn (로컬 Control)

Phase 2 라우팅 테이블 (Redis):
  "app.example.com" → {
    proxy_name: "my-web",
    drps_instance: "drps-A:9000",
    registered_at: "2026-03-23T..."
  }
```

### Phase 2 요청 흐름

```mermaid
sequenceDiagram
    participant U as User
    participant B as drps-B
    participant R as Redis
    participant A as drps-A
    participant F as frpc-1

    U->>B: GET / Host: app.example.com
    B->>R: Lookup(app.example.com)
    R-->>B: drps_instance: drps-A

    alt 로컬
        B->>F: 직접 처리
    else 원격
        B->>A: 내부 전달
        A->>F: 워크 커넥션 전달
        F-->>A: 응답
        A-->>B: 응답
    end

    B-->>U: HTTP 응답
```

### Phase 2 추가 컴포넌트

| 컴포넌트 | 역할 |
|----------|------|
| `DistributedRouter` | ProxyRegistrar 구현 (Redis/etcd) |
| `InstanceRegistry` | drps 인스턴스 등록/발견/헬스체크 |
| `RequestForwarder` | drps 간 요청 전달 |

**Protocol Layer와 Service Layer 코드 변경 없이 Bridge Layer만 교체합니다.**

## 소스 파일

| 파일 | 역할 |
|------|------|
| `router.go` | 라우팅 테이블 (Phase 1: 인메모리) |
| `interfaces.go` | 레이어 간 계약 (ProxyRegistrar, ControlRegistry) |
| `config.go` | 서버 설정 |
