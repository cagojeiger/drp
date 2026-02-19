# 05. 안전장치

broadcast + relay 구조에서 발생할 수 있는 문제와 POC에 구현된 방어 메커니즘.

## 문제 1: seen_messages 메모리 누수

### 증상

```
서비스 운영 시간이 길어질수록:
  seen_messages = { msg_001, msg_002, ..., msg_999999 }
  → 메모리 무한 증가 → OOM
```

broadcast마다 고유 msg_id를 생성하고, 중복 방지를 위해 영구 저장했기 때문.

### 해법: TTL 기반 정리

```python
seen_messages: dict[str, float]    # msg_id → monotonic timestamp

def _mark_seen(msg_id):
    now = time.monotonic()
    if len(seen_messages) > 1000:          # 임계치 초과시만 정리
        cutoff = now - 30.0                # 30초 이내만 유지
        seen_messages = {k: v for k, v in seen_messages.items() if v > cutoff}
    seen_messages[msg_id] = now
```

```
타임라인:

t=0s     msg_001 저장
t=1s     msg_002 저장
...
t=29s    msg_999 저장  (len < 1000 → 정리 안 함)
t=30s    msg_1001 저장 (len > 1000 → 30초 이전 것 제거)
         msg_001 ~ msg_xxx 제거됨

broadcast timeout이 3초이므로, 30초면 충분한 여유.
```

### 참고한 실제 시스템

| 시스템 | 방식 |
|--------|------|
| Bitcoin | Rolling Bloom Filter (50K capacity) |
| Ethereum | Bounded Set + Random Eviction (32K cap) |
| OSPF | Sequence Number + MaxAge (3600초) |
| Memberlist | Transmit Limit (log(N)*3 후 삭제) |

POC는 Ethereum 방식(Bounded + Age)을 단순화.

## 문제 2: 동시 요청 broadcast 폭주

### 증상

```
동시에 같은 hostname으로 100개 요청:
  → WhoHas 100번 broadcast
  → 각 peer에게 100개 메시지 전송
  → 네트워크 폭주
```

### 해법: Singleflight 패턴

같은 hostname에 대한 첫 번째 요청만 broadcast, 나머지는 결과를 공유.

```
Req-1 ──► find_service("myapp")
            │
            ├─ _inflight에 "myapp" 없음
            ├─ Future 생성 → _inflight["myapp"] = Future
            └─ _broadcast("myapp") 실행

Req-2 ──► find_service("myapp")       (Req-1 진행 중)
            │
            ├─ _inflight에 "myapp" 있음
            └─ await _inflight["myapp"]   ← 같은 Future 대기

Req-3 ──► find_service("myapp")       (Req-1 진행 중)
            │
            └─ await _inflight["myapp"]   ← 같은 Future 대기

_broadcast 완료:
  Future.set_result(결과)
  → Req-1, Req-2, Req-3 모두 결과 수신
  _inflight에서 "myapp" 제거
  → 다음 요청은 새 broadcast
```

```python
async def find_service(hostname):
    if hostname in self._inflight:
        return await self._inflight[hostname]    # 공유

    shared = loop.create_future()
    self._inflight[hostname] = shared
    try:
        result = await self._broadcast(hostname)
        shared.set_result(result)
        return result
    finally:
        self._inflight.pop(hostname, None)
```

### 참고한 실제 시스템

| 시스템 | 방식 |
|--------|------|
| Go stdlib `net.Resolver` | `singleflight.Group` |
| Envoy DNS | `flat_hash_map<name, CallbackList>` |
| Nginx | linked list of waiting contexts |
| CoreDNS | simplified singleflight (uint64 keys) |

Go 프로덕션에서는 `golang.org/x/sync/singleflight`를 그대로 사용하면 된다.

## 문제 3: drpc 연결 끊김 후 stale 라우팅

### 증상

```
drpc가 죽었는데 local_map에 hostname이 남아있으면:
  → User 요청이 들어옴 → work conn 요청 → 실패 → 에러
```

### 해법: 연결 종료시 local_map 정리

```python
# drps.py — handle_client_session
try:
    while True:
        msg_type, body = await read_msg(reader)
        ...
finally:
    # ctrl 연결 끊기면 등록된 모든 서비스 제거
    for hostname in registered_hostnames:
        local_map.pop(hostname, None)
```

```
drpc 정상:  local_map["myapp"] = {ctrl_writer, work_queue, ...}
drpc 죽음:  finally 블록 → local_map에서 "myapp" 제거
재요청:     local_map에 없음 → broadcast → 없음 → 502
drpc 재시작: Login → NewProxy → local_map에 다시 등록 → 200
```

## 테스트 매핑

| 안전장치 | 테스트 |
|----------|--------|
| seen_messages 루프 방지 | F5 (triangle mesh에서 502 in <8초) |
| seen_messages TTL | 구조적 검증 (장시간 운영 시나리오는 POC 범위 밖) |
| singleflight | H5 (동시 요청 3개 → 모두 200) |
| drpc 연결 정리 | F2a (drpc kill → 502), F2b (재시작 → 200) |
