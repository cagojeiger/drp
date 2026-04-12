# HTTP 프록시 스펙

## 요청 처리 흐름

```mermaid
flowchart TB
    req[HTTP 요청<br/>Host+Path] --> lookup[Router.Lookup]
    lookup -->|RouteConfig| auth{Basic Auth?}
    auth -->|미설정| pool
    auth -->|pass| pool
    auth -->|fail| r401[401 Unauthorized]

    pool[poolLookup cfg.RunID] --> get[Pool.Get timeout]
    get -->|miss| burst[burst refill<br/>ReqWorkConn]
    burst --> wait[대기]
    wait --> conn
    get -->|hit| conn[work conn]

    conn --> wrap[wrap.Wrap<br/>StartWorkConn + AES/snappy]
    wrap --> rp[httputil.ReverseProxy]
    rp --> rewrite[Rewrite<br/>Host rewrite + custom headers<br/>URL.Host keying]
    rewrite --> send[DialContext 경유 요청 전달]
    send --> resp[응답 수신]
    resp --> modify[ModifyResponse<br/>response headers]
    modify --> relay[클라이언트로 응답 전달]
    relay --> close[conn.Close]

    resp -->|101| ws[WebSocket 업그레이드 터널링<br/>ReverseProxy 기본 처리]
```

## 핵심 최적화

| 항목 | 이전 | 개선 |
|------|------|------|
| Pool 조회 | proxyName → RangeByProxy O(N) → runID → Get | **cfg.RunID → Get O(1)** |
| AES 키 | 매 요청 DeriveKey (PBKDF2) | **시작 시 1회 계산, 캐시 전달** |
| 응답 바디 버퍼 | 매 요청 임시 버퍼 | **ReverseProxy BufferPool 재사용** |
| 라우트 격리 | 주소 충돌 가능 | **`routeDialKey(cfg)` = `Domain.Location.ProxyName.drps` 로 라우트별 idle conn 격리** |
| HTTP/2 cleartext | 미지원 | **h2c.NewHandler 기반 지원** |
| WriteMsg | 3 syscall | **1 syscall** |

## 워크 커넥션 풀

```mermaid
flowchart LR
    frpc[frpc] -->|NewWorkConn| put[Pool.Put]
    put --> ch[(channel<br/>capacity=64)]
    ch --> get[Pool.Get]
    get --> use[HTTP 요청 처리]
    use --> eager[eager refill<br/>ReqWorkConn async]
    eager --> frpc

    miss[Get miss] --> burst[burst<br/>max 2,min 8,cap/4]
    burst --> worker[refillWorker]
    worker -->|N signals| reqCh[(reqCh)]
    reqCh --> sendLoop
    sendLoop -->|ReqWorkConn batch| frpc
```

**Burst refill**: pool이 비어있을 때 1개가 아니라 N개를 한꺼번에 요청 → 지연 감소.

### StartWorkConn

워크 커넥션을 꺼낸 후 frpc에 보내는 첫 메시지.

```mermaid
flowchart LR
    pool[Pool.Get] --> swc[StartWorkConn<br/>proxy_name 평문]
    swc --> enc{UseEncryption?}
    enc -->|yes| aes[AES 래핑<br/>cached key]
    enc -->|no| comp
    aes --> comp{UseCompression?}
    comp -->|yes| snap[snappy 래핑]
    comp -->|no| http[HTTP 바이트]
    snap --> http
```

## Basic Auth

```mermaid
flowchart TB
    req[요청] --> set{HTTPUser<br/>설정됨?}
    set -->|no| pass[통과]
    set -->|yes| hdr{Authorization<br/>헤더?}
    hdr -->|없음| h401[401 + WWW-Authenticate]
    hdr -->|있음| verify{user/pass<br/>일치?}
    verify -->|no| v401[401]
    verify -->|yes| pass
```

## 헤더 조작

```mermaid
flowchart LR
    subgraph in["요청 → 백엔드"]
        direction TB
        i1[HostHeaderRewrite<br/>Host 변경]
        i2[Custom Headers<br/>headers 추가/덮어쓰기]
    end
    subgraph out["백엔드 → 응답"]
        direction TB
        o1[Response Headers<br/>response_headers 추가/덮어쓰기]
    end
    client --> in --> backend --> out --> client
```

## WebSocket

```mermaid
sequenceDiagram
    participant C as Client
    participant D as drps
    participant F as frpc
    participant B as Backend

    C->>D: GET /ws Upgrade:websocket
    D->>F: (via work conn) 요청 전달
    F->>B: 요청 전달
    B-->>F: 101 Switching Protocols
    F-->>D: 101
    D->>D: ReverseProxy 업그레이드 처리
    D-->>C: 101 + 이후 바이트 터널링
```

drps는 별도 `handleUpgrade` 함수를 두지 않고 `httputil.ReverseProxy` 경로에서 업그레이드를 처리한다.

## URL.Host keying

ReverseProxy는 `Transport` 커넥션 재사용 키로 `URL.Host`를 사용한다.  
drps는 다음 synthetic host를 사용해 라우트 단위로 idle connection pool을 분리한다.

```text
{Domain}.{Location}.{ProxyName}.drps
예) app.example.com./api.web.drps
```

이 키는 네트워크 목적지가 아니며, 실제 연결은 `DialContext`에서 워크 커넥션을 직접 가져와 처리한다.

## 에러 매핑

```mermaid
flowchart LR
    miss[도메인 미등록] --> e404[404 Not Found]
    ba[Basic Auth 실패] --> e401[401 Unauthorized]
    np[풀 없음] --> e502[502 Bad Gateway]
    wf[래핑 실패] --> e502
    nr[백엔드 무응답] --> e504[504 Gateway Timeout]
```

## 타임아웃

| 타임아웃 | 기본값 | 설명 |
|---------|--------|------|
| `WorkConnTimeout` | 10초 | 풀에서 워크 커넥션 대기 |
| `ResponseTimeout` | 0(비활성) | wrapped conn 전체 I/O deadline (`DRPS_RESPONSE_TIMEOUT_SEC`로 설정) |
| `ResponseHeaderTimeout` | 60초 | ReverseProxy Transport 응답 헤더 대기 |
| `IdleConnTimeout` | 60초 | ReverseProxy idle conn 유지 시간 |
| `MaxIdleConnsPerHost` | 5 | synthetic host 키당 idle conn 개수 |
| `ReadHeaderTimeout` | 60초 | HTTP 헤더 읽기 (slowloris 방지) |

구현:
- `internal/proxy` — Handler.ServeHTTP, ReverseProxy(Rewrite/ModifyResponse/DialContext)
- `internal/wrap` — Wrap
- `internal/pool` — Pool, Registry, refillWorker
