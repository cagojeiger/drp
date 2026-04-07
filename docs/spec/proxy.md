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

### 구현

`internal/proxy` — Handler.ServeHTTP, connTransport, handleUpgrade
`internal/wrap` — Wrap (StartWorkConn + AES + snappy)
`internal/pool` — Pool, Registry
