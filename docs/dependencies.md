# drps 의존성 분석

## 배경

frp 프로젝트는 30개 이상의 직접 의존성을 사용합니다.
drps는 HTTP 프록시만 지원하므로 이 중 5개만 필요합니다.

## 필요한 라이브러리

| 라이브러리 | 버전 | 용도 |
|-----------|------|------|
| `github.com/fatedier/golib` | >= v0.5.1 | AES 암호화(crypto), 데이터 암호화/압축(io) |
| `github.com/fatedier/frp/pkg/msg` | >= v0.68.0 | 메시지 인코딩/디코딩, Dispatcher(송수신 루프) |
| `github.com/fatedier/frp/pkg/auth` | >= v0.68.0 | MD5 토큰 인증 (GetAuthKey) |
| `github.com/fatedier/frp/pkg/util` | >= v0.68.0 | CryptoReadWriter, RandID 등 유틸리티 |
| `github.com/hashicorp/yamux` | >= v0.1.1 | TCP 멀티플렉싱 (하나의 TCP 위에 여러 논리 채널) |

### 각 라이브러리가 하는 일

**golib** — frp 저자가 만든 공통 라이브러리
- `golib/crypto`: AES-128-CFB 스트림 암호화 (DefaultSalt = "frp")
- `golib/io.WithEncryption`: work connection AES 래핑
- `golib/io.WithCompression`: work connection snappy 압축
- `golib/msg`: 메시지 프레임 인코딩 (1바이트 타입 + 8바이트 길이 + JSON)

**frp/pkg/msg** — frpc↔frps 프로토콜 메시지
- `msg.ReadMsg` / `msg.WriteMsg`: 와이어 프로토콜 읽기/쓰기
- `msg.Dispatcher`: 메시지 타입별 핸들러 라우팅 + 송수신 루프
- `msg.Login`, `msg.NewProxy`, `msg.StartWorkConn` 등 구조체

**frp/pkg/auth** — 인증
- `util.GetAuthKey`: `MD5(token + timestamp)` 계산
- `util.ConstantTimeEqString`: 타이밍 공격 방어 문자열 비교

**frp/pkg/util/net** — 네트워크 유틸리티
- `CryptoReadWriter`: 제어 채널 AES 암호화 래퍼

**yamux** — HashiCorp의 멀티플렉서
- 하나의 TCP 연결 위에 여러 스트림 생성
- Stream 0: 제어 채널, Stream 1~N: work connection

## frp에는 있지만 drps에 불필요한 라이브러리

| 라이브러리 | frp 용도 | 불필요한 이유 |
|-----------|----------|-------------|
| `gorilla/mux` | 서버 API 라우팅 | drps는 자체 Router 구현 |
| `gorilla/websocket` | WebSocket 파싱 | httputil.ReverseProxy가 처리 |
| `quic-go` | QUIC 전송 | TCP만 사용 |
| `xtaci/kcp-go` | KCP 전송 | TCP만 사용 |
| `pion/stun` | NAT traversal (xtcp) | HTTP 전용 |
| `armon/go-socks5` | SOCKS5 프록시 | HTTP 전용 |
| `pires/go-proxyproto` | PROXY protocol | 미지원 |
| `prometheus/client_golang` | 메트릭 수집 | Phase 1 불필요 |
| `spf13/cobra` | CLI 프레임워크 | 단순 main으로 충분 |
| `pelletier/go-toml` | TOML 설정 파싱 | 코드/환경변수로 설정 |
| `coreos/go-oidc` | OIDC 인증 | 토큰 인증만 사용 |
| `songgao/water` | TUN 디바이스 | HTTP 전용 |
| `vishvananda/netlink` | 네트워크 인터페이스 | HTTP 전용 |
| `wireguard` | VPN 터널 | HTTP 전용 |
| `k8s.io/*` | Kubernetes 연동 | Phase 1 불필요 |
| `onsi/ginkgo,gomega` | BDD 테스트 프레임워크 | Go 표준 testing 사용 |
| `golang.org/x/oauth2` | OAuth 인증 | 토큰 인증만 사용 |
| `golang.org/x/time` | Rate limiting | Phase 1 불필요 |

## 요약

```
frp 전체 직접 의존성: 30+개
drps 필요 의존성:     5개

불필요한 90%는 TCP/UDP/QUIC/KCP/SOCKS5/WireGuard/K8s 등
HTTP 전용 drps에서 사용하지 않는 프로토콜과 기능입니다.
```
