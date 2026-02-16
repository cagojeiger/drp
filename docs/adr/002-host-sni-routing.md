# ADR-002: 호스트 기반 라우팅

## 상태
Accepted

## 컨텍스트

포트 기반 라우팅(frp 방식)의 문제:

- 서비스마다 고유 포트 → 포트 고갈
- 방화벽에서 포트 개별 허용 필요
- 분산 환경에서 포트 할당 조율 복잡

## 결정

**HTTP Host 헤더와 TLS SNI로 호스트 기반 라우팅. 공개 포트는 :80, :443 두 개만.**

### 라우팅 방식

| 포트 | 추출 | 호환 프로토콜 |
|------|------|-------------|
| :80 | HTTP 첫 요청의 `Host:` 헤더 | HTTP/1.x, WebSocket, h2c |
| :443 | TLS ClientHello의 SNI 필드 | HTTPS, WSS, gRPC, h2 |

### SNI 패스스루

:443은 TLS를 **종료하지 않는다**:

1. ClientHello에서 SNI만 읽음
2. 나머지 바이트는 그대로 work connection으로 전달
3. end-to-end TLS 보존 (서버는 평문을 볼 수 없음)

### 의사 코드

```
handleHTTP(conn):
    request = read_http_request(conn)
    hostname = extract_host(request)
    work_conn = find_and_connect(hostname)
    forward(request, work_conn)
    relay(conn, work_conn)

handleTLS(conn):
    hostname, raw_bytes = peek_sni(conn)
    work_conn = find_and_connect(hostname)
    send(work_conn, raw_bytes)
    relay(conn, work_conn)
```

## 결과

### 장점
- 포트 2개로 무제한 서비스
- end-to-end TLS 보존
- 방화벽 규칙 단순 (80, 443만)

### 단점
- 순수 TCP 서비스 라우팅 불가 (hostname 정보 없음)
- SNI 없는 구형 TLS 클라이언트 미지원
- DNS 와일드카드 설정 필요

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| 포트 기반 (frp) | 포트 고갈, 방화벽 복잡, 분산 시 조율 비용 |
| URL 경로 기반 | HTTP에서만 가능. TLS/gRPC/WebSocket 미지원 |

## 참고 자료
- [RFC 6066 - TLS SNI](https://tools.ietf.org/html/rfc6066)
- [ADR-001](./001-scope-and-philosophy.md)
