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

**HTTP/HTTPS 전용, 인프라 의존성 없는, 검증된 기술 기반 분산 리버스 프록시.**

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
| 인프라 의존성 제로 | Redis, etcd, Consul 등 외부 서비스 불필요. 바이너리 하나로 동작 |
| 검증된 기술 우선 | 자체 설계 최소화. 업계에서 검증된 프로토콜과 라이브러리 채택 |
| 인프라 중립 | K8s, Docker, bare metal 어디서든 |
| 투명한 파이프 | 프록시는 바이트를 이해하지 않고 흘려보낸다 |

### 의존성 정책

drp는 **인프라 의존성**과 **라이브러리 의존성**을 구분한다.

| 구분 | 정의 | 정책 |
|------|------|------|
| 인프라 의존성 | 별도 프로세스로 운영해야 하는 외부 서비스 (Redis, etcd, Consul, Kafka) | **금지** |
| 라이브러리 의존성 | 바이너리에 컴파일되는 Go 패키지 | **허용** (조건부) |

**핵심**: 바이너리 하나를 배포하면 끝. 별도 서비스를 운영할 필요 없다.

#### 라이브러리 허용 조건

1. **표준 프로토콜 구현**: RFC 또는 업계 표준 논문 기반
2. **프로덕션 검증**: 대규모 프로덕션 사용 사례 존재
3. **대체 가능**: 동일 프로토콜의 다른 구현으로 교체 가능

| 라이브러리 | 프로토콜 | 검증 사례 | 역할 |
|-----------|---------|----------|------|
| quic-go | QUIC (RFC 9000) | Caddy, cloudflared, Syncthing | mesh relay 전송 |
| hashicorp/memberlist | SWIM+Gossip (논문 기반) | Consul, Nomad, Serf | 멤버십 + 서비스 검색 |
| google/protobuf | Protocol Buffers | Kubernetes, gRPC, Google 인프라 | 메시지 직렬화 |

### 왜 "검증된 기술 우선"인가

자체 설계의 위험:

| 자체 설계 | 문제 | 검증된 대안 |
|----------|------|-----------|
| WhoHas/IHave broadcast | 장애 감지 없음, O(N) broadcast | SWIM+Gossip: O(log N) 수렴, 자동 장애 감지 |
| TLV+JSON 프레이밍 | 스키마 없음, 하위 호환성 보장 어려움 | Protocol Buffers: 스키마 진화, 타입 안전 |
| 자체 멀티플렉싱 | HTTP/2 스펙 = 96페이지 | QUIC: RFC 9000, 네이티브 스트림 |

검증된 기술 = 수천 개의 프로덕션 환경에서 발견·수정한 버그 + 성능 최적화를 공짜로 얻는 것.

## 결과

### 장점
- 코드 이해·유지보수 용이
- HTTP/HTTPS에 집중하여 라우팅 단순
- 인프라 없이 분산 동작
- 검증된 프로토콜로 안정성 확보

### 단점
- SSH, DB 등 순수 TCP 미지원
- frp의 고급 기능(대시보드, 플러그인, STCP/XTCP) 없음
- 라이브러리 3개 의존 (quic-go, memberlist, protobuf)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| frp 포크 후 수정 | 36,000줄 유지보수 부담. 구조 변경 불가 |
| 모든 프로토콜 지원 | frp와 같은 복잡도에 도달 |
| SaaS (Cloudflare Tunnel 등) | 자체 호스팅 필요. 인프라 제어권 확보 불가 |
| 모든 것 자체 설계 | 미검증 프로토콜. 버그 발견·수정 비용 과다 |

## 참고 자료
- [fatedier/frp](https://github.com/fatedier/frp)
- [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared)
- [hashicorp/memberlist](https://github.com/hashicorp/memberlist) — Go SWIM+Gossip 구현
- [google/protobuf](https://github.com/protocolbuffers/protobuf) — Protocol Buffers
- [quic-go](https://github.com/quic-go/quic-go) — Go QUIC 구현
