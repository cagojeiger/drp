# ADR-001: 스코프와 철학

## 상태
Accepted

## 컨텍스트

NAT 뒤 서비스를 외부에 노출하는 기존 도구(frp)의 한계:

- **36,000줄, 238파일** — 과도한 복잡도
- **단일 서버** — ControlManager 등 in-memory 상태. 분산 불가
- **포트 기반 라우팅** — 서비스마다 포트 하나. 포트 고갈
- **모든 프로토콜 지원** — TCP/UDP/HTTP/KCP/QUIC/WS. 범용성 = 복잡성

## 결정

**HTTP/HTTPS 전용, 외부 의존성 없는, 분산 리버스 프록시.**

### 한다

| 프로토콜 | 포트 | 라우팅 |
|---------|------|--------|
| HTTP/1.x, WebSocket | :80 | Host 헤더 |
| HTTPS, WSS, gRPC, h2 | :443 | TLS SNI |

### 안 한다

- 순수 TCP (hostname 정보 없어 라우팅 불가)
- UDP
- 플러그인, 대시보드, 복잡한 설정 파일

### 핵심 원칙

| 원칙 | 의미 |
|------|------|
| 하나만 잘 한다 | HTTP/HTTPS 웹 트래픽만 |
| 외부 의존성 제로 | 바이너리 하나로 동작. Redis, etcd 불필요 |
| 인프라 중립 | K8s, Docker, bare metal 어디서든 |
| 기술 비종속 | 아키텍처가 특정 언어/라이브러리에 묶이지 않음 |
| 투명한 파이프 | 프록시는 바이트를 이해하지 않고 흘려보낸다 |

## 결과

### 장점
- 코드 이해·유지보수 극도로 쉬움
- HTTP/HTTPS에 집중하여 라우팅 단순
- 외부 인프라 없이 분산 동작

### 단점
- SSH, DB 등 순수 TCP 미지원
- frp의 고급 기능(대시보드, 플러그인, STCP/XTCP) 없음

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| frp 포크 후 수정 | 36,000줄 유지보수 부담. 구조 변경 불가 |
| 모든 프로토콜 지원 | frp와 같은 복잡도에 도달 |
| SaaS (Cloudflare Tunnel 등) | 자체 호스팅 필요. 인프라 제어권 확보 불가 |

## 참고 자료
- [fatedier/frp](https://github.com/fatedier/frp)
- [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared)
