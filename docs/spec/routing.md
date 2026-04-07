# 라우팅 스펙

## 라우팅 테이블

도메인 + 경로 → RouteConfig 매핑.

```
RouteConfig {
    Domain, Location, ProxyName, RunID
    UseEncryption, UseCompression
    HTTPUser, HTTPPwd
    HostHeaderRewrite
    Headers, ResponseHeaders
}
```

## 도메인 매칭

### 우선순위

1. 정확한 도메인 (`app.example.com`)
2. 와일드카드 (`*.example.com`)

```
요청 Host: app.example.com

1. exact["app.example.com"] → 있으면 사용
2. wildcard["example.com"] → 있으면 사용 (*.example.com)
3. 없으면 → 404
```

### 와일드카드 규칙

- `*.example.com` → `foo.example.com` O, `bar.example.com` O
- `*.example.com` → `example.com` X (서브도메인 없음)
- `*.example.com` → `foo.bar.example.com` X (2단계 이상)

## 경로 매칭

Longest prefix match.

```
등록: /api/v2, /api, /
요청: /api/v2/items → /api/v2
요청: /api/users   → /api
요청: /about       → /
```

## 등록/해제

- **Add**: 도메인+경로 중복 시 에러
- **Remove(proxyName)**: 해당 프록시의 모든 도메인+경로 일괄 제거
- **연결 끊김**: 해당 frpc가 등록한 모든 프록시 자동 제거

## Pool 조회 경로

```
기존: ServeHTTP → Lookup → cfg.ProxyName → RangeByProxy(proxyName) O(N) → runID → Registry.Get
개선: ServeHTTP → Lookup → cfg.RunID → Registry.Get(runID) O(1)
```

RangeByProxy 메서드는 제거. Lookup 결과에 RunID가 이미 포함되어 있으므로 역방향 조회 불필요.

## 성능 주의사항 (현재 코드의 병목)

### 1. matchRoutes 전체 순회 — `router.go:133`

```go
func (r *Router) matchRoutes(routes []*RouteConfig, path string) (*RouteConfig, bool) {
    var best *RouteConfig
    bestLen := -1
    for _, rc := range routes {                          // ← 매번 전체 순회
        if strings.HasPrefix(path, rc.Location) && len(rc.Location) > bestLen {
            best = rc
            bestLen = len(rc.Location)
        }
    }
    ...
}
```

longest prefix match를 찾기 위해 **해당 도메인의 모든 경로를 정렬 없이 매번 전체 순회**. 하나의 도메인에 경로가 N개면 O(N).

현재 규모에서는 문제없으나, 경로 수가 증가하면 매 요청마다 비용 증가. 정렬된 슬라이스 + 이진 검색, 또는 trie 구조로 개선 가능.

### 2. RangeByProxy 선형 스캔 — `router.go:111`

```go
func (r *Router) RangeByProxy(proxyName string, fn func(cfg *RouteConfig)) {
    for _, routes := range r.exact {           // ← exact 전체
        for _, rc := range routes {            // ← 각 도메인의 전체 경로
            if rc.ProxyName == proxyName { ... }
        }
    }
    for _, routes := range r.wildcard { ... }  // ← wildcard 전체
}
```

proxyName으로 RunID를 찾기 위해 **exact + wildcard의 모든 라우트를 순회**. O(전체 라우트 수). `proxy.Handler.ServeHTTP`의 `poolLookup` 클로저에서 매 HTTP 요청마다 호출.

**근본 원인**: Lookup 결과에 이미 RunID가 포함되어 있는데, poolLookup이 proxyName을 받는 시그니처라 역방향 조회가 필요. 시그니처를 runID 기반으로 변경하면 이 메서드 자체가 불필요.

### 3. Remove 전체 순회 — `router.go:66`

```go
func (r *Router) Remove(proxyName string) {
    removeFrom := func(store map[string][]*RouteConfig) {
        for key, routes := range store {    // ← 전체 도메인 순회
            ...
        }
    }
    removeFrom(r.exact)
    removeFrom(r.wildcard)
}
```

proxyName으로 제거 시 모든 도메인의 모든 경로를 순회. 단, Remove는 연결 해제 시에만 호출되므로 hot path는 아님.

### 구현

`internal/router` — Router.Add, Remove, Lookup
