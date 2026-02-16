# ADR-004: 프로토콜과 메시지

## 상태
Accepted

## 컨텍스트

컴포넌트 간 통신 규약이 필요하다:

- 클라이언트 ↔ 서버 (제어 + 데이터)
- 서버 ↔ 서버 (mesh + relay)

## 결정

**TLV(Type-Length-Value) + JSON.**

### 메시지 프레임

```
┌──────────┬──────────────┬──────────────────┐
│ Type     │ Length       │ Body             │
│ 1 byte   │ 4 bytes (BE) │ JSON (variable)  │
└──────────┴──────────────┴──────────────────┘
```

### 메시지 타입

```
클라이언트 ↔ 서버 (제어):

  'L' Login            인증 요청
  'l' LoginResp        인증 응답
  'P' NewProxy         서비스 등록
  'p' NewProxyResp     등록 결과
  'R' ReqWorkConn      work connection 요청
  'W' NewWorkConn      work connection 제공
  'S' StartWorkConn    relay 시작
  'H' Ping             heartbeat
  'h' Pong             heartbeat 응답

서버 ↔ 서버 (mesh):

  'M' MeshHello        mesh 연결 초기화 + peer 교환
  'F' WhoHas           서비스 검색 broadcast
  'I' IHave            서비스 검색 응답
  'O' RelayOpen        relay stream 요청
```

### 포트 설계

| 포트 | 용도 | 접근 |
|------|------|------|
| :80 | HTTP 사용자 트래픽 | 공개 |
| :443 | HTTPS 사용자 트래픽 (SNI) | 공개 |
| :9000 | 제어 + mesh (이중 용도) | LB 경유(제어) / 내부(mesh) |

:9000 첫 메시지 타입으로 연결 종류 구분:

```
first_byte == 'L' → 클라이언트 연결 (Login)
first_byte == 'M' → 서버 mesh 연결 (MeshHello)
```

### 연결 다중화

하나의 TCP 연결 위에 여러 논리 스트림:

```
TCP connection
  ├── stream 0: 제어 (Login, Heartbeat, ...)
  ├── stream 1: work connection #1
  ├── stream 2: work connection #2
  └── stream N: ...
```

다중화 구현은 아키텍처에 종속되지 않음 (yamux, HTTP/2, 자체 구현 등).

### 보안

```
Layer 1: 전송 암호화 (TLS)
  └─ 클라이언트 ↔ 서버, 사용자 ↔ 서버 :443 (SNI 패스스루)

Layer 2: 인증
  └─ API Key (Login 메시지)

Layer 3: 인가
  └─ API Key별 허용 서비스 목록 (ACL)
```

### 타임아웃

| 항목 | 기본값 | 목적 |
|------|--------|------|
| Idle | 5분 | 유휴 연결 정리 |
| Heartbeat 간격 | 30초 | 제어 연결 생존 확인 |
| Heartbeat 실패 | 90초 (3회) | 연결 종료 → 자동 재연결 |

## 결과

### 장점
- 파싱 최소 (1바이트 switch + JSON unmarshal)
- 디버깅 용이 (JSON 평문)
- 어떤 언어로든 구현 가능

### 단점
- JSON은 바이너리 포맷 대비 파싱 비용 높음 (제어 메시지 빈도 낮아 무시 가능)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| protobuf | 코드 생성 도구 의존. 디버깅 불편 |
| gRPC | HTTP/2 필수. 바이너리 relay 전환 부자연스러움 |
| msgpack | JSON 대비 이점 미미. 디버깅 불편 |

## 참고 자료
- [ADR-001](./001-scope-and-philosophy.md)
- [ADR-003](./003-server-mesh-and-discovery.md)
