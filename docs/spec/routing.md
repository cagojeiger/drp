# 라우팅 스펙

## 라우팅 테이블

도메인 + 경로 → `RouteConfig` 매핑.

```mermaid
classDiagram
    class RouteConfig {
        +Domain string
        +Location string
        +ProxyName string
        +RunID string
        +UseEncryption bool
        +UseCompression bool
        +HTTPUser string
        +HTTPPwd string
        +HostHeaderRewrite string
        +Headers map~string,string~
        +ResponseHeaders map~string,string~
    }
```

## 도메인 매칭 우선순위

```mermaid
flowchart TB
    req[요청 Host: app.example.com] --> s1{exact<br/>app.example.com?}
    s1 -->|hit| match[RouteConfig]
    s1 -->|miss| s2{wildcard<br/>*.example.com?}
    s2 -->|hit| match
    s2 -->|miss| nf[404 Not Found]
```

### 와일드카드 규칙

```mermaid
flowchart LR
    pat["*.example.com"] --> r1["foo.example.com ✓"]
    pat --> r2["bar.example.com ✓"]
    pat --> r3["example.com ✗<br/>(서브도메인 없음)"]
    pat --> r4["foo.bar.example.com ✗<br/>(2단계 이상)"]
```

## 경로 매칭 — Longest Prefix

```mermaid
flowchart TB
    subgraph registered["등록된 경로"]
        direction LR
        p1["/api/v2"]
        p2["/api"]
        p3["/"]
    end

    r1["/api/v2/items"] --> p1
    r2["/api/users"] --> p2
    r3["/about"] --> p3
```

같은 도메인에 대해 가장 긴 prefix 우선.

## 등록/해제

```mermaid
stateDiagram-v2
    [*] --> Empty
    Empty --> Registered: Add(cfg)
    Registered --> Registered: Add(cfg)<br/>다른 도메인
    Registered --> Error: Add 중복
    Error --> Registered
    Registered --> Empty: Remove(proxyName)<br/>일괄 제거
    Registered --> Empty: 연결 끊김<br/>자동 제거
```

- **Add**: 도메인+경로 중복 → 에러
- **Remove(proxyName)**: 해당 프록시의 모든 도메인+경로 일괄 제거
- **연결 끊김**: frpc가 등록한 모든 프록시 자동 제거

## Pool 조회 경로

```mermaid
flowchart LR
    subgraph old["기존 O(N)"]
        direction LR
        o1[ServeHTTP] --> o2[Lookup] --> o3[cfg.ProxyName]
        o3 --> o4[RangeByProxy<br/>O N]
        o4 --> o5[runID] --> o6[Registry.Get]
    end
    subgraph new["개선 O(1)"]
        direction LR
        n1[ServeHTTP] --> n2[Lookup] --> n3[cfg.RunID]
        n3 --> n4[Registry.Get]
    end
    old -.->|변경| new
```

`RangeByProxy` 메서드는 제거. `Lookup` 결과에 이미 `RunID`가 포함되어 있으므로 역방향 조회 불필요.

구현: `internal/router` — Router.Add, Remove, Lookup
