# ADR-003: Host/SNI Routing

## 상태
Accepted

## 컨텍스트

frp는 포트 기반 라우팅을 사용한다. 서비스마다 고유 포트 하나를 차지하므로:
- 서비스 수가 늘면 포트 고갈 문제 발생
- 각 포트를 방화벽에서 개별 허용해야 함
- 분산 환경에서 포트 할당 조율이 복잡

drp는 공개 포트를 최소화하면서 다수의 서비스를 호스팅해야 한다.

## 결정

**HTTP Host 헤더와 TLS SNI(Server Name Indication)를 사용한 호스트 기반 라우팅을 채택한다.**

공개 포트는 `:80`(HTTP)과 `:443`(HTTPS) 두 개만 사용한다.

### 라우팅 방식

| 포트 | 방식 | 추출 대상 | 프로토콜 호환성 |
|------|------|----------|---------------|
| `:80` | HTTP Host 헤더 | 첫 HTTP 요청의 `Host:` | HTTP/1.0, 1.1, WebSocket |
| `:443` | TLS SNI | ClientHello의 SNI 필드 | HTTPS, h2, gRPC, WSS, 모든 TLS |

### :80 라우팅 (HTTP Host)

```go
func (s *Server) handleHTTPConn(conn net.Conn) {
    br := bufio.NewReader(conn)
    req, err := http.ReadRequest(br)
    hostname := stripPort(req.Host)
    route := s.registry.GetByHostname(hostname)
    workConn := s.getWorkConn(route)
    req.Write(workConn)
    replay(br, workConn)
    relay(workConn, conn)
}
```

### :443 라우팅 (TLS SNI)

```go
func (s *Server) handleTLSConn(conn net.Conn) {
    hostname, initBytes := peekSNI(conn)
    route := s.registry.GetByHostname(hostname)
    workConn := s.getWorkConn(route)
    workConn.Write(initBytes)
    relay(workConn, conn)
}
```

- :443은 TLS를 종료하지 않고 **SNI 패스스루**로 동작
- ClientHello의 SNI 필드만 읽고, 나머지 바이트는 그대로 work conn으로 전달
- end-to-end TLS가 보존됨 (drps는 평문을 볼 수 없음)

### 프로토콜 호환성 매트릭스

| 프로토콜 | :80 | :443 |
|---------|-----|------|
| HTTP/1.0, 1.1 | O | - |
| HTTP/2 over TLS (h2) | - | O |
| HTTP/2 cleartext (h2c upgrade) | O | - |
| WebSocket (ws) | O | - |
| WebSocket over TLS (wss) | - | O |
| gRPC over TLS | - | O |
| 임의 TLS 프로토콜 | - | O |

### DNS 설정

```
*.example.com → L4 LB 공인 IP (와일드카드 A 레코드)
```

## 결과

### 장점
- 공개 포트 2개로 무제한 서비스 호스팅
- 포트 고갈 문제 완전 해결
- 분산 환경에서 포트 조율 불필요
- :443 SNI 패스스루로 end-to-end TLS 보존
- 방화벽 규칙 단순화 (80, 443만 허용)

### 단점
- HTTP/TLS가 아닌 순수 TCP 서비스는 라우팅 불가 (hostname 정보 없음)
- SNI 없는 구형 TLS 클라이언트 미지원
- DNS 와일드카드 설정 필요

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| 포트 기반 라우팅 (frp 방식) | 포트 고갈, 방화벽 복잡, 분산 시 조율 비용 |
| URL 경로 기반 라우팅 | HTTP에서만 가능. TLS/gRPC/WebSocket 미지원 |
| PROXY protocol | 클라이언트-서버 양쪽 모두 지원해야 하는 제약 |

## 참고 자료
- [RFC 6066 - TLS SNI](https://tools.ietf.org/html/rfc6066)
- drp CONTEXT.md §5
- [ADR-001](./001-minimalist-architecture.md)
