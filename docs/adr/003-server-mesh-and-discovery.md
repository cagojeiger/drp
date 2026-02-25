# ADR-003: 서버 메시와 서비스 검색

## 상태
Accepted

## 컨텍스트

### 문제

```
User → LB → Server-B (클라이언트 없음)
              ↓
           어떻게 클라이언트를 찾아서 연결할까?
```

클라이언트(drpc)는 NAT 뒤에서 LB를 통해 서버 1대에만 연결한다.
사용자 요청이 **다른 서버**에 도착하면 어떻게 처리하나?

### 왜 mesh가 필요한가?

#### frp: 서버 1대 — 문제 없음

서버가 1대. User도, frpc도 **같은 서버**로 간다. 라우팅 문제가 없다.

#### drp: 서버 N대 — 문제 발생

서버가 N대. LB가 User를 **아무 서버나**로 보낸다.
drpc는 drps-A에 연결되어 있는데, User가 drps-B로 왔다.
**drps-B는 drpc를 모른다. 요청 처리 불가.**

### mesh가 풀어야 할 3가지 문제

| # | 문제 | 설명 |
|---|------|------|
| 1 | 멤버십 | 클러스터에 어떤 서버들이 있는가? |
| 2 | 서비스 검색 | "myapp"은 어느 서버에 있는가? |
| 3 | 장애 감지 | 서버가 죽으면 어떻게 아는가? |

기존 WhoHas/IHave broadcast 설계는 #1과 #2만 부분적으로 해결하고, #3(장애 감지)은 전혀 해결하지 못한다.

### 검토한 접근

| 접근 | 문제 |
|------|------|
| Redis/etcd | 인프라 의존. 운영 부담 |
| 자체 broadcast (WhoHas/IHave) | 장애 감지 없음, O(N) 비용, 미검증 |
| Raft 합의 | 2~10대 규모에 과한 복잡도 |
| mDNS/DNS-SD | 같은 L2 서브넷에서만 동작. 클라우드 불가 |
| CRDT (OR-Set) | 상태 동기화만 해결. 장애 감지 없음 |
| Tailscale DERP | 중앙 서버 필요 — 인프라 의존성 |

## 결정

**SWIM+Gossip 프로토콜(HashiCorp memberlist)로 멤버십, 서비스 검색, 장애 감지를 해결한다.**

### SWIM+Gossip이란

SWIM(Scalable Weakly-consistent Infection-style Process Group Membership)은 2002년 Cornell 논문에서 제안된 분산 멤버십 프로토콜이다. HashiCorp이 Consul/Serf/Nomad에서 프로덕션 검증했다.

| 구성 요소 | 역할 | 방식 |
|----------|------|------|
| SWIM | 장애 감지 | ping → indirect ping → suspect → dead |
| Gossip | 상태 전파 | 메시지를 SWIM probe에 piggyback하여 전파 |

memberlist는 SWIM 논문을 기반으로 다음을 추가 구현한다:

- **Suspicion 서브시스템**: false positive 방지 (네트워크 일시 장애 ≠ 노드 장애)
- **Push/Pull 동기화**: 주기적으로 전체 상태를 교환하여 불일치 수정
- **Dead node 재합류**: 이전에 dead로 판정된 노드가 다시 살아나면 자동 복구

### SWIM 장애 감지

```
Node-A가 Node-B를 검사하는 경우:

① 직접 Ping
    A ──ping──▶ B
    A ◀──ack─── B     ← 응답 오면 정상

② 응답 없으면 → 간접 Ping (다른 노드 경유)
    A ──ping-req──▶ C
                    C ──ping──▶ B
                    C ◀──ack─── B
    A ◀──ack──────── C     ← C를 통해 확인

③ 간접도 실패 → Suspect (의심)
    A: "B가 suspect 상태"
    gossip으로 전파 → 다른 노드들도 확인 시도

④ 타임아웃 내 반증 없으면 → Dead
    A: "B가 dead"
    gossip으로 전파 → 모든 노드가 B 제거
```

**왜 간접 Ping이 중요한가**: A↔B 사이 네트워크만 일시 장애일 수 있다. C를 경유하면 B가 실제로 살아있는지 교차 검증할 수 있다. 이것이 SWIM이 단순 heartbeat보다 **false positive가 낮은** 이유다.

### Gossip 상태 전파

SWIM probe에 상태 변경을 **piggyback**:

```
일반 SWIM ping:
    A ──[ping]──▶ B

drp의 SWIM ping (piggyback):
    A ──[ping + "C가 myapp 서비스 등록함"]──▶ B
```

추가 네트워크 비용 없이 상태가 전파된다.
수렴 시간: **O(log N)** — 5대 기준 2~3 라운드면 전체 동기화.

### 서비스 검색 흐름

```
시간순:

1. drpc가 drps-A에 연결, myapp 서비스 등록
   drps-A: localServices["myapp"] = drpc-conn

2. drps-A가 gossip으로 서비스 정보 전파
   A ──gossip──▶ B: "A has myapp"
   B ──gossip──▶ C: "A has myapp"

3. 모든 노드의 서비스 레지스트리 (eventually consistent):
   A: { myapp → local }
   B: { myapp → A }
   C: { myapp → A }

4. User → LB → drps-B
   B: registry["myapp"] → A에 있음
   B → A: QUIC relay stream 열기
   Data: User ↔ B ↔ [QUIC stream] ↔ A ↔ drpc ↔ localhost
```

**핵심 차이**: 요청 시점에 broadcast하지 않는다. 서비스 정보가 **이미 모든 노드에 있다**.

### WhoHas/IHave vs SWIM+Gossip

| | Before (WhoHas/IHave) | After (SWIM+Gossip) |
|---|---|---|
| 검색 시점 | 요청 도착 시 broadcast | 사전에 gossip으로 동기화 |
| 검색 비용 | O(N) — 모든 peer에 broadcast | **O(1)** — 로컬 레지스트리 lookup |
| 장애 감지 | 없음 | SWIM ping/indirect-ping/suspect/dead |
| 수렴 시간 | N/A (매번 broadcast) | **O(log N)** — gossip 전파 |
| 검증 수준 | 자체 설계 (미검증) | **Consul (10,000+ 노드 프로덕션)** |
| False positive | N/A | Suspect 단계로 최소화 |

### 동작 흐름 (상세)

#### 1. Node Join

```
drps-B 시작:
    drps-B --join drps-A:7946

memberlist가 SWIM join protocol 수행:
    B ──join──▶ A
    A: member list에 B 추가
    A ──member-list──▶ B    (기존 멤버 목록 전달)
    B: A, C, ... 인식
```

`--join` 은 seed node 1개만 알면 충분. 나머지는 memberlist가 자동으로 peer를 발견한다.

#### 2. Service Register

```
drpc → drps-A: Login + NewProxy{alias="myapp", hostname="myapp.example.com"}

drps-A:
    1. localServices["myapp"] = drpc-conn
    2. memberlist delegate → NotifyMsg 호출
    3. gossip 전파: ServiceUpdate{node="A", action="add", alias="myapp"}

gossip 전파 (O(log N)):
    Round 1: A → B    (B가 registry["myapp"]=A 기록)
    Round 2: B → C    (C가 registry["myapp"]=A 기록)
    → 5대 기준 2~3 라운드면 전체 수렴
```

#### 3. Request Routing

```
User → LB → drps-B: HTTP Host: myapp.example.com

drps-B:
    1. hostname → service alias 매핑 확인
    2. registry["myapp"] → 로컬? → 아님, A에 있음
    3. drps-A에 QUIC relay stream 열기 (ADR-005)
    4. RelayOpen{alias="myapp"} 전송

drps-A:
    1. localServices["myapp"] → drpc-conn 확보
    2. drpc에 ReqWorkConn 요청
    3. drpc → localhost TCP 연결

Data relay:
    User ↔ drps-B ↔ [QUIC stream] ↔ drps-A ↔ drpc ↔ localhost
```

#### 4. Node Failure

```
drps-A 장애 발생:

SWIM 감지:
    B ──ping──▶ A    → 응답 없음
    B ──ping-req──▶ C
                    C ──ping──▶ A    → 응답 없음
    B: A를 suspect로 전환
    gossip 전파: "A is suspect"

타임아웃 후:
    B: A를 dead로 전환
    gossip 전파: "A is dead"
    모든 노드: registry에서 A의 모든 서비스 제거

drpc 측:
    연결 끊김 감지 → backoff 재연결 → LB → 다른 서버(B 또는 C)에 연결
    → 새 서버에서 서비스 재등록 → gossip 재전파
```

### memberlist 설정

```go
config := memberlist.DefaultLANConfig()
config.Name = nodeID
config.BindPort = 7946                          // SWIM probe (UDP) + state sync (TCP)
config.Delegate = &drpDelegate{...}             // 서비스 정보 전파
config.Events = &drpEventDelegate{...}          // join/leave 이벤트 처리
```

| 설정 | 기본값 | 용도 |
|------|--------|------|
| BindPort | 7946 | SWIM probe (UDP) + state sync (TCP) |
| ProbeInterval | 1s | ping 간격 |
| ProbeTimeout | 500ms | ping 응답 대기 |
| SuspicionMult | 4 | suspect → dead 대기 배수 |
| GossipInterval | 200ms | gossip 전파 간격 |

memberlist는 자체 UDP(SWIM probe) + TCP(state sync) 전송을 사용한다.
QUIC relay(ADR-005)와는 독립적인 채널이다.

### 포트 분리

| 포트 | 용도 | 프로토콜 | 접근 |
|------|------|---------|------|
| :7946 | SWIM+Gossip (memberlist) | UDP+TCP | 클러스터 내부 |
| :9001 | QUIC relay (ADR-005) | UDP (QUIC) | 클러스터 내부 |
| :9000 | drpc 제어 연결 | TCP | LB 경유 |
| :80 | HTTP 사용자 트래픽 | TCP | 공개 |
| :443 | HTTPS 사용자 트래픽 (SNI) | TCP | 공개 |

멤버십/장애 감지(SWIM)와 데이터 릴레이(QUIC)가 분리되어, 각 계층이 독립적으로 동작한다.

### HA (선택 옵션)

클라이언트는 **기본 1개** 서버에 연결. 필요 시 HA 활성화:

| 모드 | 서버 3대 기준 | 장점 |
|------|-------------|------|
| 기본 (1개 연결) | ~33% 직접, ~67% relay | 설정 단순 |
| HA (2개 연결) | ~67% 직접, ~33% relay | fault tolerance |

HA 연결 시 LB가 라운드로빈으로 다른 서버에 분배.
서버 장애 시 → 남은 연결로 서비스 계속 + 끊긴 연결 backoff 재연결.

### 인터페이스 분리

HTTP 프록시 로직과 mesh 로직은 완전 분리:

```
interface ServiceFinder:
    find(hostname) → (nodeID, exists)

interface RelayDialer:
    dial(nodeID) → stream
```

HTTP 라우팅은 ServiceFinder만 호출. mesh 구현 교체 가능.

## 결과

### 장점
- 인프라 의존성 제로 (memberlist = 라이브러리)
- **O(1) 서비스 검색** (로컬 레지스트리 lookup)
- **자동 장애 감지** + 서비스 자동 제거
- 자동 peer 발견 (seed node 1개만 지정)
- **Consul에서 10,000+ 노드 프로덕션 검증**

### 단점
- Eventual consistency — 서비스 등록 후 수백 ms 전파 지연
- memberlist 라이브러리 의존
- 별도 포트 필요 (7946 UDP+TCP)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| Redis/etcd | 인프라 의존. 운영 부담 |
| 자체 broadcast (WhoHas/IHave) | 장애 감지 없음, O(N) 검색, 미검증 |
| Raft 합의 | 2~10대 규모에 과한 복잡도 |
| Tailscale DERP | 중앙 서버 필요 — 인프라 의존성 |
| mDNS/DNS-SD | 같은 L2 서브넷에서만 동작. 클라우드 불가 |

## 참고 자료
- [SWIM 논문 (2002)](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf) — Cornell
- [hashicorp/memberlist](https://github.com/hashicorp/memberlist) — Go SWIM+Gossip 구현
- [hashicorp/serf](https://github.com/hashicorp/serf) — memberlist 기반 클러스터 관리
- [Consul Architecture](https://developer.hashicorp.com/consul/docs/architecture) — SWIM+Gossip 프로덕션 사례
- [ADR-001](./001-scope-and-philosophy.md) — 검증된 기술 우선 원칙
- [ADR-004](./004-protocol-and-messages.md) — Protobuf 메시지 정의
- [ADR-005](./005-mesh-transport-quic.md) — QUIC relay 전송
