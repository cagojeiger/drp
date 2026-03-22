# ADR-002: frpc 프로토콜 호환 기반 설계

## 상태
채택됨

## 배경
리버스 프록시 서버를 새로 만들되, 기존 frpc 클라이언트를 수정 없이 사용해야 합니다.
frp는 10년간 운영되며 검증된 프로토콜과 클라이언트를 가지고 있습니다.

## 결정
frp의 메시지 패키지(`pkg/msg`), 인증 패키지(`pkg/auth`), 네트워크 유틸리티(`pkg/util/net`)를
Go module로 import하여 프로토콜 호환성을 보장합니다.

## frpc 프로토콜로 인해 자동으로 정해지는 것들

frpc가 특정 방식으로 통신하기 때문에, drps도 같은 방식을 따라야 합니다:

| 제약 | 설명 |
|------|------|
| yamux 멀티플렉싱 | TCP 연결 1개 위에 여러 논리 채널을 만듦 |
| 토큰 인증 (MD5) | `MD5(토큰 + 타임스탬프)` 형식으로 검증 |
| 제어 채널 암호화 (AES-128-CFB) | 로그인 후 모든 제어 메시지를 AES로 암호화 |
| 암호화 salt | `"frp"` 고정 salt 사용 (변경 불가) |
| 메시지 형식 | 1바이트 타입 + 8바이트 길이 + JSON 본문 |
| 동기 메시지 처리 | 수신 루프에서 핸들러를 동기적으로 실행 |
| TLS (v0.68 기본 활성화) | frpc가 기본적으로 TLS 연결을 시도함 |

### 메시지 타입 바이트

| 바이트 | 이름 | 방향 |
|--------|------|------|
| `'o'` | Login | frpc→drps |
| `'1'` | LoginResp | drps→frpc |
| `'p'` | NewProxy | frpc→drps |
| `'2'` | NewProxyResp | drps→frpc |
| `'c'` | CloseProxy | frpc→drps |
| `'r'` | ReqWorkConn | drps→frpc |
| `'w'` | NewWorkConn | frpc→drps |
| `'s'` | StartWorkConn | drps→frpc |
| `'h'` | Ping | frpc→drps |
| `'4'` | Pong | drps→frpc |

### 암호화 구조

`auth.token` 하나로 세 가지를 모두 처리합니다:
1. **인증** — `MD5(token + timestamp)`
2. **제어 채널 암호화** — `AES(key=token, salt="frp")`
3. **work conn 암호화** (useEncryption=true 시) — `AES(key=token, salt="frp")`

## 장단점

- **장점**: frpc 생태계를 즉시 활용 가능, 프로토콜을 새로 설계할 필요 없음
- **단점**: frp의 약한 암호화(MD5, AES-128-CFB) 방식을 그대로 상속, 프로토콜 변경 불가
- **완화**: `useEncryption` 옵션으로 데이터 암호화 적용, 앞단 인그레스로 전송 보안 보완

## 검토했지만 채택하지 않은 대안

| 대안 | 채택하지 않은 이유 |
|------|-------------------|
| 프로토콜을 새로 설계 | frpc를 수정해야 함, 개발 비용 높음 |
| frp 코드를 통째로 복사 (fork) | 불필요한 코드가 대량 포함, 유지보수 부담 |
| gRPC 기반 프로토콜 | frpc와 호환 불가 |
