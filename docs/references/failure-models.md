# Failure Models — 장애 모델 분류 체계

## 1. Kleppmann — DDIA Chapter 8: The Trouble with Distributed Systems

**출처**: Martin Kleppmann, *Designing Data-Intensive Applications*, Chapter 8

### 네트워크 장애 5가지

| 장애 | 설명 | 핵심 |
|------|------|------|
| Request lost | 패킷이 목적지 도달 전 소실 | 송신자는 느린 응답과 구분 불가 |
| Request queued | 네트워크/수신측 과부하로 지연 후 전달 | 지연에 상한선 없음 |
| Remote node crashed | 요청 처리 전 노드 사망 | 네트워크 유실과 구분 불가 |
| Response lost | 요청은 처리됐으나 응답 소실 | 서버 상태는 변경됨, 클라이언트 모름 |
| Response delayed | 응답이 timeout 이후 도착 | 중복 처리 위험 |

> *"If you send a request to another node and don't receive a response,
> it is impossible to tell why."*

### 시스템 타이밍 모델

| 모델 | 가정 | 현실성 |
|------|------|--------|
| Synchronous | 네트워크 지연, 프로세스 정지, 시계 오차 모두 상한 있음 | 비현실적 |
| **Partially synchronous** | 대부분 동기적, 가끔 상한 초과 | **현실적 — 대부분의 실제 시스템** |
| Asynchronous | 타이밍 가정 없음 | 너무 제한적 |

### 노드 장애 모델

| 모델 | 설명 | drp 해당 여부 |
|------|------|---------------|
| **Crash-stop** | 정지 후 복귀 안 함 | SWIM이 감지해야 함 |
| **Crash-recovery** | 정지 후 재시작, 메모리 상태 소실 | SWIM이 재합류 처리 |
| Byzantine | 임의의/악의적 메시지 전송 | drp 범위 밖 (신뢰 인프라) |

### 프로세스 일시정지 (SWIM false positive의 원인)

- GC stop-the-world (Go GC 포함)
- VM live migration (hypervisor가 프로세스 중지)
- OS 스케줄링 (수십 ms 선점)
- 동기 디스크 I/O
- 메모리 스왑

> *"A node in a distributed system must assume that its execution can be paused
> for a significant amount of time at any point, even in the middle of a function."*

### 시계 문제

| 문제 | drp 영향 |
|------|----------|
| NTP 점프 (시간이 뒤로) | timeout 계산 오류 |
| 시계 드리프트 (수정 발진기) | 누적 오차 |
| VM 시계 가상화 | VM 스케줄링 중 점프 |

**Kleppmann 권고**: timeout에는 반드시 monotonic clock 사용, 절대 time-of-day clock 사용 금지.

---

## 2. Tanenbaum — Distributed Systems Chapter 8: Fault Tolerance

**출처**: Maarten van Steen, Andrew Tanenbaum, *Distributed Systems: Principles and Paradigms*, Chapter 8

### 장애 모델 위계 (아래로 갈수록 심각)

```
Crash failure (fail-stop)
    ↓
Omission failure
  ├── Receive omission  (메시지가 서버에 도달 안 함)
  └── Send omission     (서버가 처리했으나 응답 소실)
    ↓
Timing failure
  └── 지정 시간 내 응답 실패 (성능 장애)
    ↓
Response failure
  ├── Value failure           (잘못된 값 반환)
  └── State transition failure (서버가 잘못된 동작)
    ↓
Arbitrary failure (Byzantine)
  └── 임의 시점에 임의 출력
```

### 장애 지속 분류

| 유형 | 설명 | drp 의미 |
|------|------|----------|
| **Transient** | 1회 발생 후 소멸 | 재시도로 해결 |
| **Intermittent** | 나타났다 사라졌다 반복 | 가장 진단 어려움; SWIM false positive |
| **Permanent** | 컴포넌트 교체 전까지 지속 | 노드 축출 필요 |

### 장애 마스킹 — 중복성 3가지

| 유형 | 방법 | drp 적용 |
|------|------|----------|
| Information redundancy | 체크섬, 에러 정정 코드 | protobuf CRC |
| Time redundancy | 멱등 작업 재시도 | QUIC relay 재연결 |
| Physical redundancy | 프로세스 복제 | 서버 mesh (다중 drps) |

---

## 3. 핵심 교훈 — drp에 적용

1. **drp는 Partially synchronous 모델**: 대부분 정상이나 가끔 timeout 초과
2. **Crash-stop + Crash-recovery 모델**: SWIM이 감지하고, 재합류 처리 필요
3. **Byzantine 제외**: 신뢰 인프라 가정 (ADR-001)
4. **Omission이 가장 중요**: TCP 끊김 = Send/Receive omission → 이걸 테스트해야 함
5. **Intermittent 장애가 가장 위험**: SWIM false positive → 건강한 노드를 죽은 것으로 판정

## 참고 링크

- DDIA 요약 노트: https://timilearning.com/posts/ddia/part-two/chapter-8/
- Tanenbaum Ch.8 강의 노트: http://csis.pace.edu/~marchese/CS865/Lectures/Chap8/Chapter8.htm
