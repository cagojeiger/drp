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

- `*.example.com` → `foo.example.com` ✅, `bar.example.com` ✅
- `*.example.com` → `example.com` ❌ (서브도메인 없음)
- `*.example.com` → `foo.bar.example.com` ❌ (2단계 이상)

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

### 구현

`internal/router` — Router.Add, Remove, Lookup, RangeByProxy
