# drp — Distributed Reverse Proxy

> NAT 뒤 HTTP/HTTPS 서비스를 외부에 노출하는 경량 분산 리버스 프록시

**이름**: drp = **D**istributed **R**everse **P**roxy
**상태**: 설계 v2 완료 (SWIM+Gossip + Protobuf + QUIC), Go 구현 진행 중

---

## 1. 한 줄 요약

NAT 뒤 HTTP 서비스를 외부에 노출하되, **인프라 의존성 없이** 검증된 기술로 수평 확장되는 리버스 프록시.

## 2. 왜 만드는가

frp(fatedier/frp)의 한계:

| 문제 | 설명 |
|------|------|
| 단일 서버 | ControlManager 등 in-memory. 분산 불가 |
| 포트 기반 라우팅 | 서비스마다 포트 하나. 포트 고갈 |
| 과도한 복잡도 | 36,000줄, 238파일. TCP/UDP/KCP/QUIC 등 |

## 3. 핵심 원칙

| 원칙 | 의미 |
|------|------|
| 하나만 잘 한다 | HTTP/HTTPS 전용 |
| 인프라 의존성 제로 | Redis, etcd, Consul 등 외부 서비스 불필요. 바이너리 하나 |
| 검증된 기술 우선 | 자체 설계 최소화. 업계 검증된 프로토콜/라이브러리 채택 |
| 인프라 중립 | K8s, Docker, bare metal 어디서든 |

## 4. 아키텍처

```
User → LB → drps (server mesh) → drpc (behind NAT) → localhost
```

### 5대 설계 결정

| # | 결정 | 요약 | ADR |
|---|------|------|-----|
| 1 | 스코프 | HTTP/HTTPS만. 인프라 의존성 제로. 검증된 기술 우선 | [001](docs/adr/001-scope-and-philosophy.md) |
| 2 | 라우팅 | Host 헤더 + TLS SNI. 포트 2개로 무제한 | [002](docs/adr/002-host-sni-routing.md) |
| 3 | 분산 | SWIM+Gossip (memberlist) 멤버십 + 서비스 검색 | [003](docs/adr/003-server-mesh-and-discovery.md) |
| 4 | 프로토콜 | Protocol Buffers. 스키마 진화. 타입 안전 | [004](docs/adr/004-protocol-and-messages.md) |
| 5 | 전송 | QUIC (drps↔drps relay). 네이티브 멀티플렉싱 | [005](docs/adr/005-mesh-transport-quic.md) |

### 핵심 흐름: Gossip 전파 → 로컬 검색 → QUIC 릴레이

drp의 분산 로직은 세 단계로 환원된다:

```
1. 전파:   Client → Server-A 등록 → gossip으로 전체 클러스터에 전파
2. 검색:   Server-B: 로컬 레지스트리에서 myapp → Server-A 확인 (O(1))
3. 릴레이: User ↔ Server-B ↔ [QUIC stream] ↔ Server-A ↔ Client ↔ localhost
```

## 5. frp 대비 차이

| | frp | drp |
|---|---|---|
| 아키텍처 | 단일 서버 | 분산 (server mesh) |
| 라우팅 | 포트 기반 | Host/SNI (포트 2개) |
| 외부 의존성 | 없음 (단일 서버라 불필요) | 인프라 의존성 없음 (라이브러리만: quic-go, memberlist, protobuf) |
| 프로토콜 | TCP/UDP/HTTP/KCP/QUIC/WS | HTTP/HTTPS 전용 |
| HA | 없음 | Mesh 라우팅 + 선택적 HA |

## 6. 참고 소스

- frp 원본: `.repos/frp/` (git submodule)
- ADR: `docs/adr/`
