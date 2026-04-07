# HTTP 프록시 스펙

## 요청 처리 흐름

```
클라이언트 → HTTP :httpAddr → drps
  1. Router.Lookup(Host, Path) → RouteConfig (RunID 포함)
  2. Basic Auth 검증 (HTTPUser 설정 시)
  3. poolLookup(cfg.RunID) → Pool 직접 획득 (이중 조회 없음)
  4. Pool.Get() → 워크 커넥션 획득
  5. wrap.Wrap(conn, cachedKey, ...) → StartWorkConn + 암호화/압축 (캐시된 키)
  6. Custom Headers 주입
  7. HostHeaderRewrite 적용
  8. req.Write → frpc → 백엔드
  9. http.ReadResponse → 클라이언트에 전달 (bufio sync.Pool 재사용)
  10. Response Headers 주입
  11. 워크 커넥션 닫기 + eager refill
```

### 이전 대비 변경점

| 항목 | 이전 | 이후 |
|------|------|------|
| Pool 조회 | poolLookup(proxyName) → RangeByProxy O(N) → runID → Get | poolLookup(cfg.RunID) → Get O(1) |
| AES 키 | 매 요청 DeriveKey(token) PBKDF2 계산 | 서버 시작 시 1회 계산, 캐시 전달 |
| bufio.Reader | 매 요청 새로 할당 | sync.Pool 재사용 |
| WriteMsg | 3회 Write (3 syscall) | 1회 Write (1 syscall) |

## 워크 커넥션 풀

- RunID 기반 풀 관리 (하나의 frpc = 하나의 풀)
- capacity: 설정 가능 (기본 64)
- **Get**: 즉시 시도 → 없으면 ReqWorkConn 전송 → 대기 → 타임아웃
- **eager refill**: Get 성공 후 비동기로 ReqWorkConn 전송

### StartWorkConn

워크 커넥션을 꺼낸 후 frpc에 전송하는 첫 메시지.

```
StartWorkConn {proxy_name} → 평문으로 전송
이후 UseEncryption → AES 래핑 (캐시된 키 사용)
이후 UseCompression → snappy 래핑
이후 HTTP 바이트 교환
```

## Basic Auth

```
RouteConfig.HTTPUser가 설정된 경우:
  → Authorization 헤더 없음 → 401 + WWW-Authenticate
  → user/pass 불일치 → 401
  → 일치 → 통과
```

## 헤더 조작

### 요청 → 백엔드

| 기능 | 설정 | 동작 |
|------|------|------|
| HostHeaderRewrite | `host_header_rewrite` | Host 헤더 변경 |
| Custom Headers | `headers` | 요청에 헤더 추가/덮어쓰기 |

### 백엔드 → 응답

| 기능 | 설정 | 동작 |
|------|------|------|
| Response Headers | `response_headers` | 응답에 헤더 추가/덮어쓰기 |

## WebSocket

```
클라이언트 → GET /ws Upgrade:websocket → drps
drps → 워크 커넥션으로 전달 → frpc → 백엔드
백엔드 → 101 Switching Protocols
drps → Hijack → 양방향 raw byte relay
양쪽 goroutine 모두 완료 대기 후 정리
```

- 101 응답 감지 → 클라이언트 연결을 Hijack
- 이후 양방향 io.Copy (drps는 바이트를 그대로 전달)
- 양쪽 relay goroutine이 모두 종료된 후 연결 닫기 (goroutine leak 방지)

## 에러 처리

| 상황 | HTTP 상태 |
|------|-----------|
| 도메인 미등록 | 404 Not Found |
| Basic Auth 실패 | 401 Unauthorized |
| 풀 없음 / 워크 커넥션 획득 실패 | 502 Bad Gateway |
| 래핑 실패 | 502 Bad Gateway |
| 백엔드 무응답 (타임아웃) | 504 Gateway Timeout |

## 타임아웃

| 타임아웃 | 기본값 | 설명 |
|---------|--------|------|
| WorkConnTimeout | 10초 | 풀에서 워크 커넥션 대기 |
| ResponseTimeout | 설정 가능 | 백엔드 응답 대기 |

## 성능 주의사항 (현재 코드의 병목)

### 1. poolLookup 이중 조회 — `main.go:39`, `router.go:111`

현재 `ServeHTTP`의 Pool 조회 경로:

```
Router.Lookup(host, path) → RouteConfig { ProxyName, RunID, ... }
                                            ↓
poolLookup(proxyName) → RangeByProxy(proxyName)  ← O(N) 전체 라우트 순회!
                                            ↓
                                        runID 획득
                                            ↓
                                    registry.Get(runID)
```

`RangeByProxy` (`router.go:111`)는 exact + wildcard 맵의 모든 라우트를 순회하며 `proxyName`이 일치하는 첫 번째를 찾는다. **Lookup이 이미 RunID를 반환하는데 다시 proxyName→RunID 역방향 조회를 하는 이중 조회 구조.**

프록시 수가 증가하면 매 HTTP 요청마다 선형으로 성능 저하.

### 2. PBKDF2 매 요청 호출 — `wrap.go:30`, `crypto.go:21`

```
매 HTTP 요청 → wrap.Wrap(conn, token, ...) → crypto.DeriveKey(token)
                                                ↓
                                        PBKDF2(SHA1, 64 iter) 실행
```

동일 토큰에서 항상 같은 키가 나오는데, 매 요청마다 CPU 바운드 키 파생을 반복.

### 3. 매 요청 hot path의 상세 비용

```
ServeHTTP 1회 호출 시:
  ├─ Router.Lookup         : RLock + map lookup + matchRoutes 순회 + RUnlock
  ├─ log.Printf            : 뮤텍스 + fmt + syscall (2회)
  ├─ poolLookup            : RangeByProxy O(N) + RLock/RUnlock
  ├─ Pool.Get              : mutex + chan recv + go requestFn()
  ├─ log.Printf            : 뮤텍스 + fmt + syscall (2회)
  ├─ wrap.Wrap             : WriteMsg (3 syscall) + DeriveKey (PBKDF2)
  │                          + rand.Read(IV) + NewCipher + ReadFull(IV)
  ├─ log.Printf            : 뮤텍스 + fmt + syscall (1회)
  ├─ r.Clone               : deep copy of *http.Request
  ├─ req.Write             : 직렬화 + Write
  ├─ bufio.NewReader(conn) : 4KB 힙 할당
  └─ http.ReadResponse     : 파싱 + body 읽기
```

총 5회 log.Printf (각각 뮤텍스 + fmt + 최소 1 syscall), 3회 WriteMsg syscall, 1회 PBKDF2, 1회 RangeByProxy O(N) 순회가 **매 HTTP 요청마다** 발생.

### 구현

`internal/proxy` — Handler.ServeHTTP, connTransport, handleUpgrade
`internal/wrap` — Wrap (StartWorkConn + AES + snappy)
`internal/pool` — Pool, Registry
