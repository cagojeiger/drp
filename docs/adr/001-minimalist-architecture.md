# ADR-001: Minimalist Architecture — HTTP/HTTPS Only

## 상태
Accepted

## 컨텍스트

frp(fatedier/frp)는 NAT 뒤 서비스를 외부에 노출하는 훌륭한 도구지만 다음과 같은 한계가 있다:

- **36,000줄, 238개 Go 파일** — 너무 크고 복잡
- **단일 서버 한계** — ControlManager, proxy.Manager, workConnCh 모두 in-memory. 분산 불가
- **포트 기반 라우팅** — 서비스마다 포트 하나씩 차지. 포트 고갈 문제
- **자체 암호화 레이어** — CryptoReadWriter + Compression + ContextConn 래핑
- **goroutine 무제한 생성** — MaxConnection 제한 없음
- **모든 프로토콜 지원** — TCP/UDP/HTTP/KCP/QUIC/WS 등. 범용성이 복잡성의 원인

drp는 **웹 트래픽(HTTP/HTTPS)만** 프록시하는 경량 대안을 목표로 한다.

## 결정

**HTTP/HTTPS 전용, ~750줄, 10파일의 최소주의 아키텍처를 채택한다.**

### 스코프 제한

drp가 하는 것:

| 프로토콜 | 포트 | 라우팅 |
|---------|------|--------|
| HTTP/1.0, 1.1 | :80 | `Host` 헤더 |
| WebSocket (ws) | :80 | `Host` 헤더 |
| HTTPS | :443 | TLS SNI |
| WebSocket (wss) | :443 | TLS SNI |
| gRPC over TLS | :443 | TLS SNI |
| HTTP/2 (h2) | :443 | TLS SNI |

drp가 하지 않는 것:
- 순수 TCP 포워딩 (SSH, DB 등) — hostname 정보 없어 라우팅 불가
- UDP — 다른 프로토콜
- KCP, QUIC — 불필요한 복잡도

### 핵심 원칙

| 원칙 | 설명 |
|------|------|
| **HTTP/HTTPS 전용** | 웹 트래픽 하나만 제대로 한다 (Unix 철학) |
| **분산 우선** | 수평 확장이 기본 설계. LB 뒤에서 동작 |
| **TLS 표준** | Go 표준 라이브러리 crypto/tls 사용. 자체 암호화 없음 |
| **투명한 파이프** | 프록시는 바이트를 이해하지 않고, 그냥 흘려보낸다 |

### frp 대비 차이

| | frp | drp |
|---|---|---|
| 코드 | 36,000줄, 238파일 | **~750줄, 10파일** |
| 아키텍처 | 단일 서버 | **분산 (Redis + 멀티 노드)** |
| 라우팅 | 포트 기반 (포트 고갈) | **Host/SNI (포트 2개로 무제한)** |
| 암호화 | 자체 구현 (CryptoRW + snappy) | **TLS (Go 표준 라이브러리)** |
| 프로토콜 | TCP/UDP/HTTP/KCP/QUIC/WS 등 | **HTTP/HTTPS 전용** |
| HA | 없음 | **HA 멀티 연결 + 자동 재연결** |
| 배포 | 단일 서버 | **K8s / LB 네이티브** |
| 설정 | TOML/YAML 복잡한 설정 | **CLI 플래그 몇 개** |

### 코드 구조

```
drp/
├── main.go         # CLI 파싱, server/client 모드    (~60줄)
├── msg.go          # 메시지 정의 + 직렬화/역직렬화    (~100줄)
├── server.go       # Accept, Login, 라우팅            (~200줄)
├── auth.go         # API Key 검증 + ACL              (~50줄)
├── registry.go     # Redis 기반 라우팅 테이블          (~90줄)
├── cluster.go      # 노드 간 relay (fallback)        (~60줄)
├── client.go       # Connect, Login, HA 연결          (~130줄)
├── relay.go        # io.Copy 양방향 + peek replay     (~40줄)
├── pool.go         # work connection pool             (~50줄)
                      ──────────────────────
                      합계: ~750줄
```

### 의존성

| 의존성 | 용도 |
|--------|------|
| Go 표준 라이브러리 | net, io, crypto/tls, http, bufio, encoding/json |
| `github.com/redis/go-redis` | Redis 클라이언트 |
| `github.com/hashicorp/yamux` | TCP 멀티플렉싱 |

## 결과

### 장점
- 코드 이해·유지보수가 극도로 쉬움 (~750줄)
- HTTP/HTTPS에 집중하여 라우팅 로직 단순
- 연결당 메모리 37% 절감 (frp 43KB → drp 27KB)
- 수평 확장 + HA 기본 지원
- K8s, LB와 자연스럽게 통합

### 단점
- SSH, DB 등 순수 TCP 서비스는 프록시 불가
- UDP 미지원
- frp의 고급 기능(대시보드, 플러그인, STCP/XTCP 등) 없음

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| frp 포크 후 수정 | 36,000줄 유지보수 부담. 10년간 쌓인 구조 변경 불가 |
| frp + 플러그인 확장 | 분산, 라우팅, HA 문제는 플러그인으로 해결 불가 |
| 모든 프로토콜 지원 | TCP/UDP까지 지원하면 frp와 같은 복잡도에 도달 |
| Cloudflare Tunnel 등 SaaS | 자체 호스팅 필요. 인프라 제어권 확보 |

## 참고 자료
- [fatedier/frp](https://github.com/fatedier/frp)
- [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared)
- drp CONTEXT.md §1, §2, §9
