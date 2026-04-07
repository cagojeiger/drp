# HTTP 프록시 스펙

## 요청 처리 흐름

```
클라이언트 → HTTP :18080 → drps
  1. Router.Lookup(Host, Path) → RouteConfig
  2. Basic Auth 검증 (HTTPUser 설정 시)
  3. Pool.Get() → 워크 커넥션 획득
  4. wrap.Wrap() → StartWorkConn + 암호화/압축
  5. Custom Headers 주입
  6. HostHeaderRewrite 적용
  7. req.Write → frpc → 백엔드
  8. http.ReadResponse → 클라이언트에 전달
  9. Response Headers 주입
  10. 워크 커넥션 닫기 + eager refill
```

## 워크 커넥션 풀

- RunID 기반 풀 관리 (하나의 frpc = 하나의 풀)
- **Get**: 즉시 시도 → 없으면 ReqWorkConn 전송 → 대기 → 타임아웃
- **eager refill**: Get 성공 후 비동기로 ReqWorkConn 전송

### StartWorkConn

워크 커넥션을 꺼낸 후 frpc에 전송하는 첫 메시지.

```
StartWorkConn {proxy_name} → 평문으로 전송
이후 UseEncryption → AES 래핑
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
```

- 101 응답 감지 → 클라이언트 연결을 Hijack
- 이후 양방향 io.Copy (drps는 바이트를 그대로 전달)

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
