# POC 계획

## 1. 증명할 것

ADR-003(서버 메시와 서비스 검색)이 **실제로 동작하는가?**

구체적으로:

| # | 가설 | 성공 기준 |
|---|------|----------|
| 1 | 서버들이 mesh를 형성한다 | 3대 서버가 서로 연결 확인 |
| 2 | Partial mesh에서도 broadcast가 도달한다 | A-B-C 직렬 연결에서 A→C 검색 성공 |
| 3 | 클라이언트가 1개 서버에만 연결해도 된다 | 다른 서버로 온 요청이 처리됨 |
| 4 | Relay로 실제 HTTP 응답이 돌아온다 | curl → drps-B → mesh → drps-A → drpc → local → 응답 |

## 2. 증명 안 할 것 (POC 밖)

| 항목 | 이유 |
|------|------|
| TLS / SNI | 검증된 기술. POC 가치 없음 |
| 인증 (API Key) | 비즈니스 로직. 아키텍처와 무관 |
| HA (멀티 연결) | mesh가 되면 HA는 연결 N개 열기일 뿐 |
| 연결 제한 / 타임아웃 | 운영 파라미터 |
| 성능 | 개념 검증. 성능은 Go 구현에서 |

## 3. 테스트 시나리오

### 시나리오 1: 로컬 히트 (기본)

```
curl http://localhost:8001 -H "Host: myapp.example.com"
     → drps-A (drpc가 여기 연결됨)
     → 로컬 pool에서 바로 처리
     → "hello from local service"
```

### 시나리오 2: 리모트 히트 (핵심)

```
curl http://localhost:8002 -H "Host: myapp.example.com"
     → drps-B (drpc 없음)
     → WhoHas broadcast
     → drps-A: IHave 응답
     → drps-B ↔ drps-A relay
     → drpc → localhost:5000
     → "hello from local service"
```

### 시나리오 3: Partial mesh (견고성)

```
drps-A ── drps-B ── drps-C (A와 C는 직접 연결 없음)
drpc → drps-A에 연결

curl http://localhost:8003 -H "Host: myapp.example.com"
     → drps-C
     → WhoHas → drps-B로 전파 → drps-A로 전파
     → drps-A: IHave → 역경로로 drps-C에 도달
     → drps-C ↔ drps-B ↔ drps-A relay
     → "hello from local service"
```

## 4. 아키텍처 선택지

### 선택지 A: asyncio (단일 이벤트 루프)

```python
# 모든 I/O가 비동기
async def handle_connection(reader, writer):
    ...
```

**장점**: Python 답게 깔끔. 동시성 자연스러움.
**단점**: asyncio 익숙해야 함.

### 선택지 B: threading + socket

```python
# 전통적 블로킹 소켓 + 스레드
def handle_connection(conn):
    ...
```

**장점**: 직관적. 네트워크 프로그래밍 기본기.
**단점**: 스레드 관리 복잡. GIL.

### 선택지 C: 프로세스 기반 (subprocess)

```python
# 각 서버/클라이언트가 별도 프로세스
# 실제 배포와 동일한 구조
```

**장점**: 실제 구조와 가장 유사.
**단점**: 디버깅 어려움.

**제안**: 선택지 A (asyncio). 이유:
- 동시 연결 처리가 자연스러움
- relay의 양방향 복사가 `asyncio.gather`로 깔끔
- Python 표준 라이브러리만 사용

## 5. 다중화 (Multiplexing) 처리

프로덕션에서는 yamux 같은 스트림 다중화가 필요하지만, POC에서는:

### 선택지 A: 다중화 없이 TCP 여러 개

```
제어 연결:  TCP #1 (Login, Heartbeat, ReqWorkConn)
work conn: TCP #2, #3, #4... (요청마다 새 연결)
```

**장점**: 단순. 개념에 집중.
**단점**: 연결 수 많아짐. NAT 환경에서 현실과 다름.

### 선택지 B: 간단한 프레임 기반 다중화

```
하나의 TCP 위에 stream_id를 붙여서 다중화:
[stream_id: 4 bytes][type: 1 byte][length: 4 bytes][body]
```

**장점**: yamux의 핵심 개념 검증.
**단점**: 직접 구현해야 함 (복잡도 증가).

### 선택지 C: 제어만 TLV, 데이터는 별도 TCP

```
제어: TLV+JSON over 단일 TCP
데이터: work conn 요청 시 새 TCP 연결
```

**장점**: 제어와 데이터 분리가 명확. 구현 단순하면서 핵심 검증 가능.
**단점**: 다중화 자체는 검증 안 됨.

**제안**: 선택지 C. 이유:
- 다중화는 POC 대상이 아님 (검증된 기술)
- mesh + broadcast + relay에 집중
- 구현 복잡도 최소화

## 6. 파일 구조 (안)

```
poc/
├── docs/
│   └── plan.md            # 이 문서
├── drps.py                # 서버 (mesh + broadcast + relay + HTTP listener)
├── drpc.py                # 클라이언트 (서버 연결 + work conn 제공)
├── protocol.py            # TLV+JSON 메시지 정의 + 읽기/쓰기
├── mesh.py                # mesh 연결 관리 + WhoHas/IHave + relay
├── run_test.sh            # 테스트 자동화 스크립트
└── README.md              # POC 실행 방법
```

## 7. 실행 방법 (안)

```bash
# 터미널 1: 로컬 서비스 (HTTP)
python -m http.server 5000

# 터미널 2: drps-A (drpc가 여기 연결)
python drps.py --node-id A --http-port 8001 --control-port 9001 --peers localhost:9002

# 터미널 3: drps-B
python drps.py --node-id B --http-port 8002 --control-port 9002 --peers localhost:9001,localhost:9003

# 터미널 4: drps-C
python drps.py --node-id C --http-port 8003 --control-port 9003 --peers localhost:9002

# 터미널 5: drpc (서버 A에 연결)
python drpc.py --server localhost:9001 --alias myapp --hostname myapp.example.com --local localhost:5000

# 테스트
curl http://localhost:8001 -H "Host: myapp.example.com"   # → 로컬 히트
curl http://localhost:8002 -H "Host: myapp.example.com"   # → 리모트 히트 (mesh relay)
curl http://localhost:8003 -H "Host: myapp.example.com"   # → partial mesh (2 hop relay)
```

## 8. 열린 질문

1. **프로세스 구조**: 서버 3대 + 클라이언트 1대를 각각 별도 프로세스로? 아니면 하나의 테스트 프로세스에서?
2. **테스트 자동화**: shell script? pytest?
3. **로깅**: 어느 수준? (메시지 단위 추적 필요?)
4. **에러 케이스**: 서버 죽었을 때 동작도 검증?
