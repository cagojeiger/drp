# 프록시 테스트

`internal/proxy`, `internal/wrap`, `internal/pool` 대상. 27개.

## 워크 커넥션 풀 (6) — internal/pool

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| P-01 | TestPutAndGet | P0 | Put → Get 즉시 반환 + eager refill |
| P-02 | TestGetEmpty | P0 | 비어있으면 requestFn 호출 후 대기 |
| P-03 | TestGetTimeout | P0 | 대기 타임아웃 → error |
| P-04 | TestClose | P1 | Close 후 Get → error, Put → 안전 |
| P-05 | TestConcurrentGetPut | P1 | 동시 10개 Get/Put race 안전 |
| P-06 | TestGetMultipleEagerRefill | P1 | Get마다 requestFn 호출 확인 |

## 커넥션 래핑 (5) — internal/wrap

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| W-01 | TestWrapStartWorkConn | P0 | StartWorkConn 메시지 전송 + ProxyName |
| W-02 | TestWrapNoEncNoComp | P0 | 평문 → 바이트 그대로 전달 |
| W-03 | TestWrapEncryption | P0 | AES 래핑 → frpc 쪽 복호화 성공 |
| W-04 | TestWrapCompression | P0 | snappy 래핑 → frpc 쪽 해제 성공 |
| W-05 | TestWrapEncAndComp | P0 | AES + snappy 조합 라운드트립 |

## HTTP 기본 (5) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-01 | TestProxyBasicRequest | P0 | GET → 200 + body 무결성 |
| X-02 | TestProxyNotFound | P0 | 미등록 도메인 → 404 |
| X-03 | TestProxyNoWorkConn | P0 | 풀 없음 → 502 |
| X-04 | TestProxyHostHeaderRewrite | P1 | Host 헤더 변경 → frpc에 전달 확인 |
| X-05 | TestProxyMultipleDomains | P0 | 2개 도메인 독립 라우팅 + 응답 |

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

## WebSocket (1) — internal/proxy

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| X-16 | TestWebSocketRelay | P0 | 101 → Hijack → 양방향 echo |
