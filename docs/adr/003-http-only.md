# ADR-003: HTTP 프록시만 지원

## 상태
채택됨

## 배경
frp는 HTTP, HTTPS, TCP, UDP 등 다양한 프록시 타입을 지원합니다.
모든 타입을 한 번에 구현하면 복잡도가 급격히 증가합니다.

실제 운영 환경에서는 drps 앞에 인그레스/로드밸런서를 두고
TLS를 처리하는 구조가 일반적이므로, drps가 직접 TLS를 다룰 필요가 없습니다.

## 결정
HTTP 프록시만 지원합니다.
지원하지 않는 타입(https, tcp, udp 등)은 에러 메시지로 거부합니다.

전송 구간 암호화가 필요한 경우 frpc의 `transport.useEncryption = true` 옵션을
사용하면 work connection이 AES로 암호화됩니다.

## 지원하는 기능

| 기능 | frpc 설정 |
|------|-----------|
| 커스텀 도메인 | `customDomains` |
| 서브도메인 | `subdomain` |
| 경로 라우팅 | `locations` |
| 와일드카드 도메인 | `*.example.com` |
| Host 헤더 변경 | `hostHeaderRewrite` |
| 요청/응답 헤더 주입 | `requestHeaders.set` / `responseHeaders.set` |
| HTTP Basic Auth | `httpUser` / `httpPassword` |
| 클라이언트 IP 전달 | 자동 (X-Forwarded-For) |
| 응답 타임아웃 | 서버 설정 |
| WebSocket | 자동 (101 Switching Protocols) |
| work conn 암호화 | `transport.useEncryption` |
| work conn 압축 | `transport.useCompression` |

## 암호화 정책

drps는 앞단 인그레스에 TLS를 맡기므로 자체 TLS를 구현하지 않습니다.
frpc↔drps 사이 데이터 암호화는 frpc 설정에 맡기되, `useEncryption = true`를 권장합니다.

```toml
# frpc.toml 권장 설정
transport.tls.enable = false        # drps가 TLS 미지원이므로 비활성화
transport.useEncryption = true      # work conn AES 암호화 (권장)
transport.useCompression = true     # 압축 (선택)
```

| 설정 조합 | 결과 |
|-----------|------|
| tls=OFF, useEncryption=ON | 적절한 조합 (권장) |
| tls=OFF, useEncryption=OFF | work conn 평문 (비권장) |
| tls=ON | drps가 TLS 미지원이므로 연결 실패 |

drps는 두 경우(암호화 ON/OFF) 모두 처리하며, 서버에서 강제하지 않습니다.

## 거부하는 프록시 타입

HTTP 외 타입(`https`, `tcp`, `udp`, `stcp`, `sudp`, `xtcp`, `tcpmux`)은
에러 메시지로 거부합니다.

## 검토했지만 채택하지 않은 대안

| 대안 | 채택하지 않은 이유 |
|------|-------------------|
| 모든 프록시 타입 동시 구현 | 복잡도가 높아 개발이 지연됨 |
| TCP 프록시를 먼저 구현 | HTTP의 도메인 기반 라우팅이 더 높은 가치를 제공 |
| drps에서 직접 TLS 처리 | 앞단 인그레스와 역할 중복, 불필요한 복잡성 |
