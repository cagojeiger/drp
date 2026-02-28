# Testing Methodology — 분산 시스템 테스팅 방법론

## 1. OSDI 2014 — Simple Testing Can Prevent Most Critical Failures

**출처**: Ding Yuan 외, USENIX OSDI 2014
**분석 대상**: Cassandra, HBase, HDFS, MapReduce, Redis, ZooKeeper의 198개 프로덕션 장애

### 핵심 발견

| 발견 | 수치 | 의미 |
|------|------|------|
| 에러 핸들링 버그 | **92%** | 치명적 장애의 92%는 비정상 경로(에러 핸들링)에서 발생 |
| 1~2 노드 재현 | **77%** | 대부분 복잡한 멀티노드 시나리오 불필요 |
| 결정적 재현 | **58%** | 같은 입력이면 항상 동일 장애 발생 |
| 단일 이벤트 트리거 | **35%** | 복합 이벤트 시퀀스 불필요 |

### drp에 대한 함의

> **에러 핸들링 코드의 유닛 테스트가 가장 효과적이다.**

구체적으로:
- `net.Conn.Write()`가 에러 반환할 때 어떻게 되는가?
- SWIM이 잘못된 gossip 메시지 받으면 어떻게 되는가?
- QUIC 스트림이 중간에 reset되면 어떻게 되는가?
- TCP control connection이 활성 proxy 세션 중 끊기면 어떻게 되는가?

이 모든 것이 mock connection으로 유닛 테스트 가능하다.

### 논문 링크

https://www.usenix.org/system/files/conference/osdi14/osdi14-paper-yuan.pdf

---

## 2. Jepsen — 장애 주입 표준 분류

**출처**: Kyle Kingsbury, Jepsen 프레임워크
**소스코드**: https://github.com/jepsen-io/jepsen (commit `0d4e417`)

### 6대 장애 카테고리

```clojure
;; jepsen/src/jepsen/nemesis/combined.clj
(defn nemesis-packages [opts]
  (let [faults (:faults opts [:partition :packet :kill :pause :clock :file-corruption])]
    ...))
```

#### 1) `:partition` — 네트워크 파티션

| 토폴로지 | 설명 |
|----------|------|
| `:one` | 단일 노드 격리 (비대칭) |
| `:majority` | 과반수/소수 분할 (대칭) |
| `:majorities-ring` | 링 형태 겹치는 과반수 |
| `:minority-third` | 1/3 미만 분리 |
| `:primaries` | 프라이머리 노드만 격리 |

#### 2) `:packet` — 패킷 레벨 조작

```clojure
;; jepsen/src/jepsen/net.clj — tc netem 매핑
(def all-packet-behaviors
  {:delay     {:time :50ms, :jitter :10ms, :correlation :25%}
   :loss      {:percent :20%, :correlation :75%}
   :corrupt   {:percent :20%, :correlation :75%}
   :duplicate {:percent :20%, :correlation :75%}
   :reorder   {:percent :20%, :correlation :75%}
   :rate      {:rate :1mbit}})
```

#### 3) `:kill` — 프로세스 크래시 (SIGKILL)
#### 4) `:pause` — 프로세스 일시정지 (SIGSTOP → SIGCONT)
#### 5) `:clock` — 시계 왜곡

```clojure
;; jepsen/src/jepsen/nemesis/time.clj
{:f :bump,   :value {node1 delta-ms}}       ; 시계 점프
{:f :strobe, :value {node1 {:delta ms ...}}} ; 시계 진동
{:f :reset}                                  ; NTP 복원
```

#### 6) `:file-corruption` — 저장소 손상

### Jepsen NATS 2.12.1 분석 (2025.12)

> *"프로세스 일시정지 + 네트워크 지연 복합 장애가 committed write 유실과
> 영구 split-brain을 유발한다."*

복합 장애(compound fault)가 단독 장애보다 위험하다.

### Jepsen 소스 링크

- nemesis 카테고리: https://github.com/jepsen-io/jepsen/blob/0d4e417/jepsen/src/jepsen/nemesis/combined.clj
- 패킷 조작: https://github.com/jepsen-io/jepsen/blob/0d4e417/jepsen/src/jepsen/net.clj
- 시계 조작: https://github.com/jepsen-io/jepsen/blob/0d4e417/jepsen/src/jepsen/nemesis/time.clj

---

## 3. Netflix Chaos Engineering

**출처**: https://principlesofchaos.org/

### 4단계 방법론

1. **Steady state 정의**: 정상 동작의 측정 가능한 출력 (요청 성공률, latency p99, SWIM 수렴 시간)
2. **가설 수립**: 장애 주입 시에도 steady state 유지될 것이다
3. **실제 장애 변수 주입**: 하드웨어 장애, 소프트웨어 장애, 비장애 이벤트 (트래픽 급증)
4. **가설 반증**: control 그룹과 실험 그룹 사이 차이 발견

### Netflix Simian Army

| 도구 | 주입 장애 |
|------|----------|
| Chaos Monkey | 랜덤 인스턴스 종료 |
| Chaos Gorilla | 전체 가용 영역(AZ) 장애 |
| Chaos Kong | 전체 리전 장애 |
| Latency Monkey | 인위적 네트워크 지연 |

### drp 관련 핵심 인용

> *"체계적 약점: 서비스 불가 시 잘못된 fallback, 부적절한 timeout으로 인한
> retry storm, 하류 의존성 과부하 시 outage, 단일 장애점 크래시 시 cascading failure."*

→ SWIM false positive로 인한 retry storm, QUIC relay failover under partition

---

## 4. Google SRE — Chapter 17: Testing for Reliability

**출처**: https://sre.google/sre-book/testing-reliability/

### 테스트 피라미드

```
Unit Tests          → ms,  네트워크 없음, 결정적
Integration Tests   → s,   mock 의존성
System Tests        → min, 풀 스택
  ├── Smoke tests
  ├── Performance tests
  └── Regression tests
Production Tests    → 실 트래픽
  ├── Configuration tests
  ├── Stress tests
  └── Canary tests
```

### 카오스 테스트에 대한 Google의 견해

> *"Chaos Monkey와 Jepsen 같은 통계적 기법은 반드시 반복 가능하지 않다.
> 코드 변경 후 재실행해도 관찰된 장애가 수정되었다고 단정할 수 없다."*

**Google 권고**: 카오스 테스트의 랜덤 시드/액션 시퀀스를 기록하여 결정적 재현 가능하게.

---

## 5. Partial Network Partitioning (OSDI 2020)

**출처**: "Toward a Generic Fault Tolerance Technique for Partial Network Partitioning"

### 핵심 발견

부분 파티션(일부 메시지만 소실)이 완전 파티션보다 **더 위험**하다.

시스템은 "노드 X에 연결 가능 = X로의 모든 메시지 성공"이라고 가정하지만,
현실에서는 일부만 실패할 수 있다.

MongoDB, HBase, HDFS, Kafka, RabbitMQ, Elasticsearch, Mesos에서 확인됨.

### drp 의미

SWIM gossip이 일부만 전달될 때:
- 노드 A는 서비스 등록을 봄
- 노드 B는 못 봄
- 노드 C는 일부만 봄
→ 라우팅 불일치 발생 가능

---

## 6. 종합 — 우선순위 정렬

문헌 합의에 기반한 drp 장애 테스트 우선순위:

### Tier 1 — 반드시 테스트 (가장 높은 실전 빈도)

1. **N1** 완전한 연결 끊김 → SWIM dead node 감지, QUIC relay 재연결
2. **P1** 프로세스 크래시 → 멤버십 수렴
3. **N4** 높은 지연 / timeout → SWIM false positive
4. **P3** 크래시 후 복구 → 노드 재합류, 상태 동기화

### Tier 2 — 높은 영향, 중간 빈도

5. **N9** Split-brain 파티션
6. **N2** 비대칭 파티션 (A→B OK, B→A 실패)
7. **P2** 프로세스 일시정지 (GC, VM migration)
8. **N3** 패킷 손실

### Tier 3 — 엣지 케이스

9. **N5** 패킷 재정렬
10. **N6** 패킷 중복
11. **C1/C2** 시계 왜곡
12. **X1** 일시정지 + 파티션 복합 장애

## 참고 자료 모음

- OSDI 2014 논문: https://www.usenix.org/system/files/conference/osdi14/osdi14-paper-yuan.pdf
- Jepsen: https://jepsen.io/
- Principles of Chaos: https://principlesofchaos.org/
- Google SRE Ch.17: https://sre.google/sre-book/testing-reliability/
- 분산 시스템 테스팅 큐레이션: https://github.com/asatarin/testing-distributed-systems
