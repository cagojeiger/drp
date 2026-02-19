# 99. 프로덕션 요구사항

POC에서 검증된 것과 별개로, 실제 운영에 필요한 항목들.

## 요약

| # | 항목 | 없으면 어떻게 되나 | 우선순위 |
|---|------|-------------------|----------|
| 1 | 서비스 캐시 | 매 요청마다 broadcast → 지연 + 트래픽 폭주 | **P0** |
| 2 | Peer heartbeat + reconnect | 죽은 peer 감지 불가 + 영구 단절 | **P0** |
| 3 | Backpressure | fd/conn 고갈 → 서버 전체 장애 | **P0** |
| 4 | Graceful shutdown | 진행 중 요청 끊김 + peer 에러 감지 지연 | P1 |
| 5 | Observability | 장애 원인 파악 불가 | P1 |
| 6 | Peer 간 인증 | 아무나 mesh에 참여 가능 | P1 |
| 7 | Config reload | 설정 변경시 재시작 필요 | P2 |

---

## 1. 서비스 캐시 (P0)

### 현재 상태

모든 remote 요청이 broadcast를 거친다. 같은 hostname이라도 singleflight이 끝나면 다음 요청은 또 broadcast.

```
현재:   요청 → broadcast (3초 대기) → relay
필요:   요청 → cache hit → relay (즉시)
        요청 → cache miss → broadcast → relay → cache 저장
```

### 필요한 것

| 캐시 종류 | 저장 내용 | TTL | 무효화 조건 |
|-----------|----------|-----|------------|
| **Positive** | hostname → (node_id, relay_path) | 5~10초 | drpc disconnect 이벤트 |
| **Negative** | hostname → not_found | 1~2초 | 새 서비스 등록 이벤트 |

### 왜 필요한가

- Positive cache 없으면: 초당 1000 요청 × 전체 peer 수 = mesh 트래픽 폭주
- Negative cache 없으면: 없는 hostname 요청마다 3초 대기. DDoS 벡터가 됨

### 설계 영향

캐시 무효화를 위해 **서비스 등록/해제 이벤트 broadcast**가 필요할 수 있다. 현재 프로토콜에 없는 새 메시지 타입:

```
ServiceUp   → "myapp이 node A에 등록됨" → 모든 peer의 positive cache에 저장
ServiceDown → "myapp이 node A에서 해제됨" → 모든 peer의 cache에서 삭제
```

이건 WhoHas/IHave 패턴과 다르다. broadcast가 아니라 **proactive push**.

---

## 2. Peer Heartbeat + Reconnect (P0)

### 현재 상태

```
peer 죽음 → _peer_loop에서 read 실패 → peers에서 제거 → 끝
재연결 시도 없음. 수동 재시작 필요.
```

### 필요한 것

```
Heartbeat:
  매 5초마다 Ping 전송
  10초 내 Pong 없으면 → dead 판정 → 연결 정리

Reconnect:
  dead 판정 후 → 1초, 2초, 4초, 8초... (exponential backoff)
  최대 60초 간격으로 재시도
  재연결 성공 → MeshHello 교환 → 정상 peer로 복귀
```

### 왜 필요한가

- Heartbeat 없으면: 한쪽만 끊긴 half-open 상태 감지 불가. 계속 write 시도 → 에러 누적
- Reconnect 없으면: 네트워크 순간 끊김에도 영구 단절. 운영자 개입 필요

### 설계 영향

프로토콜에 Ping/Pong 메시지 타입 추가 필요. 또는 TCP keepalive 옵션 사용.

---

## 3. Backpressure (P0)

### 현재 상태

```
동시 relay 연결 수 → 제한 없음 → fd 고갈 가능
work conn 생성 수 → 제한 없음 → drpc 과부하 가능
broadcast fan-out → peer 수에 비례 → 제한 없음
```

### 필요한 것

| 자원 | 제한 방법 |
|------|----------|
| 동시 relay 연결 | semaphore (예: 최대 1000개) |
| hostname당 work conn pool | bounded queue (예: 최대 50개) |
| broadcast 동시 처리 | singleflight (이미 있음) + rate limit |
| TCP 연결 총 수 | ulimit + 프로세스 레벨 카운터 |

### 왜 필요한가

제한 없으면 트래픽 폭증시 서버 전체가 죽는다. 한 서비스의 과부하가 같은 서버의 다른 서비스에 영향.

---

## 4. Graceful Shutdown (P1)

### 현재 상태

```
서버 종료: kill → 즉시 죽음 → 진행 중 요청 끊김
peer 측: read 실패로 감지 (지연 있음)
drpc 측: 연결 끊기면 재연결 시도 (현재 구현 안 됨)
```

### 필요한 것

```
1. SIGTERM 수신
2. 새 연결 수신 중단 (listener 닫기)
3. peer들에게 "나 내려간다" 알림
4. 진행 중 요청 완료 대기 (timeout 있음)
5. 모든 연결 정리
6. 종료
```

### 왜 필요한가

- 배포시 rolling update에서 요청 손실 방지
- peer가 즉시 dead를 인지 → 불필요한 retry/timeout 방지

---

## 5. Observability (P1)

### 현재 상태

Python `logging` 모듈로 텍스트 로그만 출력. 구조화된 메트릭 없음.

### 필요한 것

| 메트릭 | 용도 |
|--------|------|
| `drp_broadcast_total` | broadcast 빈도 모니터링 |
| `drp_singleflight_hits` | singleflight 효율 측정 |
| `drp_relay_hops` (histogram) | relay 경로 길이 분포 |
| `drp_seen_messages_size` | 메모리 사용 추적 |
| `drp_peer_connections` | mesh 상태 |
| `drp_find_service_duration` (histogram) | 서비스 검색 지연시간 |
| `drp_active_relays` | 동시 relay 수 |
| `drp_work_conn_pool_size` | work conn pool 상태 |

추가로:
- 구조화된 로그 (JSON)
- 분산 트레이싱 (요청 → broadcast → IHave → relay 체인 추적)

### 왜 필요한가

- broadcast가 느린데 왜? → `find_service_duration` 확인
- 메모리가 늘어나는데 왜? → `seen_messages_size` 확인
- 특정 서비스만 느린데 왜? → 트레이싱으로 relay 경로 확인

---

## 6. Peer 간 인증 (P1)

### 현재 상태

제어 포트에 TCP 연결 후 MeshHello만 보내면 peer로 등록된다. 인증 없음.

### 필요한 것

```
옵션 A: Pre-shared key
  MeshHello에 token 포함 → 서버에서 검증

옵션 B: mTLS
  peer 연결을 TLS로 감싸고, 인증서로 상호 인증

옵션 C: HMAC 챌린지
  서버가 nonce 전송 → peer가 shared secret으로 HMAC 응답
```

### 왜 필요한가

인증 없으면:
- 악의적 서버가 mesh에 참여 → 트래픽 도청/변조
- 가짜 IHave 응답 → 요청을 악성 서버로 유도
- relay를 통한 내부 네트워크 접근

---

## 7. Config Reload (P2)

### 현재 상태

CLI 인자로 설정. 변경시 프로세스 재시작 필요.

### 필요한 것

```
SIGHUP → 설정 파일 재읽기 → 적용
또는
config watch (파일 변경 감지)

변경 가능 항목:
  - peer 목록 (동적 peer 추가/제거)
  - broadcast timeout
  - backpressure 임계치
  - 로그 레벨
```

### 왜 필요한가

- peer 추가시 서버 재시작 → downtime
- 운영 중 튜닝 (timeout, 제한값) → 재시작 없이 변경하고 싶음

---

## POC → 프로덕션 갭 요약

```
POC (검증됨)                    프로덕션 (추가 필요)
──────────────                  ──────────────────
mesh broadcast ✓                + 서비스 캐시
seen_messages ✓                 + heartbeat/reconnect
singleflight ✓                  + backpressure
relay ✓                         + graceful shutdown
drpc disconnect ✓               + observability
                                + peer 인증
                                + config reload
```
