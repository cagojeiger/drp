# frps HTTP 전용 코드 경로 분석

frps v0.68.0에서 HTTP 프록시에만 관련된 코드를 추출하여 분석합니다.
drps 구현 시 어떤 코드를 참고하고 어떤 것을 생략할지 판단하기 위한 자료입니다.

## 파일별 HTTP 관련 코드량

| 파일 | 전체 줄 | HTTP 관련 줄 | 비율 | 역할 |
|------|---------|-------------|------|------|
| `server/service.go` | 700 | ~35 | 5% | HTTP 리스너 생성 |
| `server/control.go` | 543 | ~100 | 18% | work conn 풀, 프록시 등록 |
| `server/proxy/http.go` | 167 | 167 | 100% | HTTP 프록시 등록/커넥션 |
| `server/proxy/proxy.go` | 396 | ~120 | 30% | work conn 획득, StartWorkConn |
| `pkg/util/vhost/http.go` | 316 | 316 | 100% | HTTPReverseProxy |
| `pkg/util/vhost/router.go` | 137 | 137 | 100% | domain/location 라우팅 |
| `pkg/util/vhost/vhost.go` | 305 | ~35 | 11% | RouteConfig 타입 정의 |
| `pkg/util/vhost/resource.go` | 88 | 88 | 100% | 404 페이지 |
| **합계** | **2,652** | **~998** | **38%** | |

frps 전체 코드 중 HTTP에 필요한 것은 약 **1,000줄** (38%)입니다.

## HTTP 요청 전체 경로

```
외부 HTTP 요청
     │
     ▼
① net.Listen("tcp", ":8080")                    ← service.go
     │
     ▼
② http.Server{Handler: HTTPReverseProxy}         ← service.go
     │
     ▼
③ HTTPReverseProxy.ServeHTTP()                   ← vhost/http.go
   ├─ CheckAuth() (Basic Auth 검증)
   ├─ injectRequestInfoToCtx()
   │   ├─ Host, Path, HTTPUser 추출
   │   └─ Routers.Get()으로 RouteConfig 조회
   │       └─ domain → location longest-prefix match
   └─ httputil.ReverseProxy.ServeHTTP()
       │
       ▼
④ Transport.DialContext()                         ← vhost/http.go
   └─ CreateConnection()
       └─ RouteConfig.CreateConnFn()
           │
           ▼
⑤ HTTPProxy.GetRealConn()                        ← proxy/http.go
   ├─ GetWorkConnFromPool()                       ← proxy/proxy.go
   │   └─ Control.GetWorkConn()                   ← control.go
   │       ├─ workConnCh에서 꺼내기 (non-blocking)
   │       └─ 없으면 ReqWorkConn → frpc → 대기
   ├─ msg.WriteMsg(StartWorkConn{})
   ├─ [옵션] libio.WithEncryption()
   └─ [옵션] libio.WithCompression()
       │
       ▼
⑥ httputil.ReverseProxy가 work conn 통해
   frpc ↔ 로컬 서비스 간 HTTP 요청/응답 릴레이
```

## 핵심 5개 컴포넌트

drps가 반드시 구현해야 하는 최소 단위:

### 1. Routers (라우팅 테이블)

```
구조: domain → httpUser → []*Router (location 내림차순 정렬)

Register: domain+location 조합 등록, longest-prefix를 위해 정렬
Lookup:   domain → location prefix match → RouteConfig 반환
Delete:   proxyName 기준 제거
```

- 소스: `pkg/util/vhost/router.go` (137줄, 통째로 참고 가능)
- 와일드카드 매칭: `*.example.com` → `*.com` → `*` 순차 탐색

### 2. HTTPReverseProxy (HTTP 요청 처리)

```
구조: httputil.ReverseProxy 래핑

ServeHTTP: 요청 수신 → 라우팅 → Basic Auth → ReverseProxy
Transport.DialContext: 라우팅 결과로 work conn 생성
Rewrite: Host 변경, 헤더 주입
ErrorHandler: 502/504 에러 처리
```

- 소스: `pkg/util/vhost/http.go` (316줄, 핵심 ~200줄)
- 32KB sync.Pool BufferPool 사용

### 3. HTTPProxy (프록시 등록/커넥션)

```
Run:        RouteConfig 생성 → HTTPReverseProxy.Register()
GetRealConn: work conn 획득 → 암호화/압축 래핑
Close:      HTTPReverseProxy.UnRegister()
```

- 소스: `server/proxy/http.go` (167줄)
- 암호화/압축: `libio.WithEncryption`, `libio.WithCompression`

### 4. Control.GetWorkConn (워크 커넥션 풀)

```
Phase 1: workConnCh에서 non-blocking 꺼내기
Phase 2: 없으면 ReqWorkConn 전송 → 타임아웃 대기
보충:    꺼낸 직후 ReqWorkConn 전송 (eager refill)
```

- 소스: `server/control.go:242-284` (~40줄)
- 풀 크기: `poolCount + 10`

### 5. GetWorkConnFromPool + StartWorkConn

```
1. Control.GetWorkConn()으로 work conn 획득
2. StartWorkConn 메시지 전송 (proxyName, srcAddr, dstAddr)
3. 실패 시 poolCount+1 번 재시도
```

- 소스: `server/proxy/proxy.go:124-175` (~50줄)

## 성능 최적화 (HTTP 경로에 적용된 것)

| 최적화 | 위치 | 효과 |
|--------|------|------|
| 32KB BufferPool (sync.Pool) | `vhost/http.go:129` | 요청당 버퍼 할당 제거 |
| HTTP Transport 커넥션 재사용 | `vhost/http.go:107-128` | `MaxIdleConnsPerHost: 5` |
| 워크 커넥션 풀 + eager refill | `control.go:282` | 지연시간 최소화 |
| location 내림차순 정렬 | `router.go:63-65` | longest-prefix O(N) 매칭 |
| 동기 메시지 핸들러 | `control.go:358-361` | 고루틴 폭탄 방지 |
| 디스패처 sendCh 버퍼 100 | `msg/handler.go:41` | 쓰기 경합 방지 |

## drps에서 생략 가능한 것

| 기능 | 줄 수 | 이유 |
|------|-------|------|
| 로드밸런싱 그룹 (HTTPGroupCtl) | ~100 | Phase 1 불필요 |
| 대역폭 제한 (Limiter) | ~20 | 비핵심 |
| 메트릭 (Prometheus) | ~30 | 운영 기능, 후순위 |
| 서버 플러그인 | ~20 | 확장 기능 |
| h2c (HTTP/2 cleartext) | ~5 | HTTP/1.1으로 충분 |
| 포트 공유 (muxer) | ~30 | 별도 포트 사용 |
| 커스텀 404 페이지 파일 | ~20 | 하드코딩으로 충분 |
| routeByHTTPUser 라우팅 | ~30 | 고급 기능 |
| NatHole 핸들러 | ~15 | HTTP와 무관 |
| CONNECT 메서드 핸들러 | ~25 | 정방향 프록시 기능 |

## drps v1 vs frps 구조 비교

| 컴포넌트 | frps | drps v1 | 차이 |
|----------|------|---------|------|
| 라우팅 | 3단계 (domain→httpUser→location) | 2단계 (domain→location) | httpUser 차원 생략 |
| ReverseProxy | httputil.ReverseProxy + h2c | httputil.ReverseProxy | h2c 생략 |
| BufferPool | 32KB sync.Pool | 4KB sync.Pool (bufio.Reader) | 크기 차이 |
| Work conn 풀 | poolCount + 10 | MaxPoolCount | 크기 계산 방식 |
| 프록시 팩토리 | reflect 기반 레지스트리 | 직접 처리 (HTTP만) | 팩토리 불필요 |
| 인터페이스 | 없음 (Service 직접 참조) | 3개 인터페이스 | drps가 더 깔끔 |
| 암호화/압축 | WithEncryption/Compression | 동일 | 동일 |
