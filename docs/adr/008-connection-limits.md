# ADR-008: Connection Limits & Resource Protection

## 상태
Accepted

## 컨텍스트

WebSocket 등 long-lived connection이 주요 유스케이스이므로, 연결 수 제한과 자원 보호가 필수다.

제한 없이 운영하면:
- 사용자 무한 접속 → work conn 무한 생성 → goroutine 폭발 → OOM
- 죽은 WebSocket이 좀비로 남음 → fd 고갈
- 서버 재시작 시 모든 WebSocket 동시 끊김

## 결정

**3단계 연결 제한 + 타임아웃 + graceful shutdown을 구현한다.**

### 3단계 연결 제한

```
Level 1: 서버 전체 (--max-connections)
  └─ drps 한 대가 감당할 총 연결 수. 기본값: 50,000

Level 2: 서비스당 (NewProxy.MaxConnections)
  └─ 하나의 alias에 대한 최대 동시 연결. 기본값: 1,000

Level 3: Work conn pool (pool 크기)
  └─ pool 가득 차면 사용자에게 503 반환
```

### 타임아웃

| 타임아웃 | 기본값 | 목적 |
|---------|--------|------|
| Idle timeout | 5분 | 데이터 없는 연결 정리 (WebSocket은 ping/pong으로 유지) |
| Read/Write deadline | 30초 | 한쪽이 응답 없을 때 정리 |
| Heartbeat interval | 30초 | drpc ↔ drps 제어 연결 생존 확인 |
| Heartbeat timeout | 90초 | 3번 실패 시 연결 종료 |

### Graceful Shutdown

```
1. LB에서 해당 drps 제거 (신규 연결 차단)
2. 기존 연결에 drain 시간 부여 (--drain-timeout, 기본 30초)
3. drain 후 남은 연결 강제 종료
4. drpc가 자동 재연결 → LB가 다른 drps로 라우팅
```

### File Descriptor 체크

```
연결 1개 = fd 2개 (user conn + work conn)
50,000 연결 = fd 100,000개

drps 시작 시:
  1. 현재 ulimit -n 확인
  2. max-connections * 2 + 여유분보다 작으면 경고 로그
```

### Backpressure

io.Copy는 자연스러운 배압(backpressure)을 제공:
- writer가 느리면 reader도 자동으로 느려짐
- TCP window가 줄어들면서 자연 배압

**중간에 channel이나 buffer queue를 넣지 않는 것이 핵심.**

## 결과

### 장점
- OOM, fd 고갈 등 자원 고갈 방지
- 좀비 연결 자동 정리
- 서버 업데이트 시 무중단 drain

### 단점
- 제한에 걸린 사용자는 503 에러
- 타임아웃 값 튜닝 필요 (환경에 따라 다름)

## 참고 자료
- [ADR-001](./001-minimalist-architecture.md)
- [ADR-007](./007-ha-connections.md)
