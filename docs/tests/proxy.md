# 프록시 테스트

`internal/proxy`, `internal/wrap`, `internal/pool` 대상. 29개 + 추가 6개 + 벤치마크 5개.

## 워크 커넥션 풀 (6) — internal/pool

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| P-01 | TestPutAndGet | P0 | Put → Get 즉시 반환 + eager refill |
| P-02 | TestGetEmpty | P0 | 비어있으면 requestFn 호출 후 대기 |
| P-03 | TestGetTimeout | P0 | 대기 타임아웃 → error |
| P-04 | TestClose | P1 | Close 후 Get → error, Put → 안전 |
| P-05 | TestConcurrentGetPut | P1 | 동시 10개 Get/Put race 안전 |
| P-06 | TestGetMultipleEagerRefill | P1 | Get마다 requestFn 호출 확인 |

### 추가 테스트

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| P-07 | TestNewWithCapacity | P0 | New(fn, 128) → 128개까지 Put 성공, 129번째 버림 |
| P-08 | TestPutOverflow | P1 | capacity 초과 Put → conn.Close 호출 확인 |

## 커넥션 래핑 (5) — internal/wrap

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| W-01 | TestWrapStartWorkConn | P0 | StartWorkConn 메시지 전송 + ProxyName |
| W-02 | TestWrapNoEncNoComp | P0 | 평문 → 바이트 그대로 전달 |
| W-03 | TestWrapEncryption | P0 | AES 래핑 → frpc 쪽 복호화 성공 |
| W-04 | TestWrapCompression | P0 | snappy 래핑 → frpc 쪽 해제 성공 |
| W-05 | TestWrapEncAndComp | P0 | AES + snappy 조합 라운드트립 |

### 추가 테스트

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| W-06 | TestWrapWithCachedKey | P0 | Wrap(conn, cachedKey, ...) — 전달받은 키로 암호화, DeriveKey 미호출 확인 |

## HTTP 기본 (5) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-01 | TestProxyBasicRequest | P0 | GET → 200 + body 무결성 |
| X-02 | TestProxyNotFound | P0 | 미등록 도메인 → 404 |
| X-03 | TestProxyNoWorkConn | P0 | 풀 없음 → 502 |
| X-04 | TestProxyHostHeaderRewrite | P1 | Host 헤더 변경 → frpc에 전달 확인 |
| X-05 | TestProxyMultipleDomains | P0 | 2개 도메인 독립 라우팅 + 응답 |

### 추가 테스트

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-17 | TestProxyPoolLookupByRunID | P0 | poolLookup(cfg.RunID) 직접 호출 → RangeByProxy 미사용 확인 |

## Transport 커넥션 풀 격리 (2) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-20 | TestURLHostKeyingIsolation | P0 | 같은 도메인 다른 Location/ProxyName → synthetic URL.Host 값이 서로 다름 |
| X-21 | TestURLHostKeyingViaProxy | P0 | 두 라우트 경유 요청 → 각각 독립 커넥션 풀로 200 응답 |

## 암호화/압축 통합 (3) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-06 | TestProxyWithEncryption | P0 | AES end-to-end (proxy → wrap → frpc) |
| X-07 | TestProxyWithCompression | P0 | snappy end-to-end |
| X-08 | TestProxyWithEncAndComp | P0 | AES + snappy end-to-end |

## 헤더/인증 (5) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-09 | TestCustomRequestHeaders | P1 | cfg.Headers → 요청에 주입 |
| X-10 | TestResponseHeaders | P1 | cfg.ResponseHeaders → 응답에 주입 |
| X-11 | TestBasicAuthSuccess | P0 | 올바른 인증 → 프록시 통과 |
| X-12 | TestBasicAuthFail | P0 | 잘못된 인증 → 401 |
| X-13 | TestBasicAuthMissing | P0 | 인증 없음 → 401 + WWW-Authenticate |

## 타임아웃 (2) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-14 | TestProxyTimeout | P0 | 백엔드 무응답 → 504 Gateway Timeout |
| X-15 | TestProxyNoTimeout | P1 | 정상 응답 시 타임아웃 미발생 |

## WebSocket (1+2) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-16 | TestWebSocketRelay | P0 | 101 → Hijack → 양방향 echo |

### 추가 테스트

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-18 | TestWebSocketBothGoroutinesComplete | P0 | 양쪽 relay goroutine 모두 종료 확인 (goroutine leak 방지) |
| X-19 | TestWebSocketServerClose | P1 | 서버 측 먼저 닫기 → 클라이언트 goroutine도 종료 |

## 벤치마크 — internal/pool

| ID | 벤치마크 | 검증 |
|----|---------|------|
| B-X-02 | BenchmarkPoolGetPut | Pool Get/Put 사이클 성능 |
| B-X-05 | BenchmarkRegistryGet | Registry.Get RunID 직접 조회 O(1) |

미구현 후보 (performance.md 참고): BenchmarkServeHTTP, BenchmarkWrap, BenchmarkConnTransportRoundTrip.
