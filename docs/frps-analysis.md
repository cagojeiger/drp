# frps 아키텍처 및 성능 분석

frps v0.68.0의 내부 구조와 성능 최적화 패턴을 분석합니다.
drps 구현 시 어떤 패턴을 가져오고 어떤 것을 생략할지 판단하기 위한 참고 자료입니다.

## 아키텍처 레이어

frps는 6개 레이어로 구성됩니다.

```
┌──────────────────────────────────────────────────────┐
│ Layer 1: Service (서버 본체)                           │
│ - TCP/KCP/QUIC/WebSocket/TLS/SSH 리스너 관리          │
│ - 메시지 타입별 분기 (Login, NewWorkConn, Visitor)     │
│ - yamux 세션 생성                                     │
└───────────────┬──────────────────────────────────────┘
                │ frpc 접속 시 생성
                ▼
┌──────────────────────────────────────────────────────┐
│ Layer 2: Control (클라이언트별 관리)                    │
│ - 워크 커넥션 풀 (buffered channel)                    │
│ - 제어 채널 암호화 (AES-128-CFB)                       │
│ - 메시지 디스패처 (Ping, NewProxy, CloseProxy)         │
│ - 하트비트 감시                                        │
└───────────────┬──────────────────────────────────────┘
                │ 프록시 등록 시 생성
                ▼
┌──────────────────────────────────────────────────────┐
│ Layer 3: Proxy (프록시 타입별 처리)                     │
│ - 팩토리 패턴으로 HTTP/HTTPS/TCP/UDP 등 분기           │
│ - 워크 커넥션 획득 + StartWorkConn 전송                │
│ - 암호화/압축/대역폭 제한 적용                          │
│ - libio.Join으로 양방향 데이터 릴레이                   │
└───────────────┬──────────────────────────────────────┘
                │ HTTP 요청 수신 시
                ▼
┌──────────────────────────────────────────────────────┐
│ Layer 4: VHost/Router (HTTP 라우팅)                    │
│ - domain → httpUser → location 3단계 매칭              │
│ - 와일드카드 도메인 순차 탐색 (*.example.com → *.com)   │
│ - httputil.ReverseProxy + h2c 지원                    │
│ - 32KB sync.Pool BufferPool                           │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ Layer 5: Group (로드밸런싱)                            │
│ - HTTP Group: atomic round-robin                      │
│ - 같은 도메인에 여러 프록시 연결 시 분배                │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ Layer 6: Authentication (인증)                         │
│ - Login, WorkConn, Ping 각각에서 토큰 검증             │
│ - MD5(token + timestamp) 기반                          │
└──────────────────────────────────────────────────────┘
```

## HTTP 프록시 요청 경로

외부 HTTP 요청이 frpc의 로컬 서비스까지 도달하는 전체 경로:

```
① 외부 HTTP 요청 도착
   http.Server.Serve(listener)

② HTTPReverseProxy.ServeHTTP()
   - Host, Path, HTTPUser 추출
   - Basic Auth 검증
   - h2c → httputil.ReverseProxy로 전달

③ ReverseProxy.Rewrite()
   - 라우팅 테이블에서 RouteConfig 조회
   - ChooseEndpointFn() (HTTP Group이면 round-robin)
   - 헤더 수정 (Host 변경, 요청 헤더 주입)

④ Transport.DialContext()
   - Routers.Get()으로 domain+location+httpUser 매칭
   - 와일드카드 도메인 순차 탐색

⑤ HTTPProxy.GetRealConn()
   - Control.GetWorkConn()으로 풀에서 워크 커넥션 획득
   - 풀에 없으면 frpc에 ReqWorkConn 전송 후 타임아웃 대기
   - StartWorkConn 메시지 전송
   - 암호화/압축 레이어 적용

⑥ 데이터 릴레이
   - httputil.ReverseProxy가 HTTP 요청을 워크 커넥션으로 전송
   - frpc가 로컬 서비스(localhost:3000)에 전달

⑦ ModifyResponse()
   - 응답 헤더 주입

⑧ 클라이언트에 HTTP 응답 전송
```

## 성능 최적화 패턴

### 커넥션 풀링

| 패턴 | 위치 | 설명 |
|------|------|------|
| 워크 커넥션 풀 | `Control.workConnCh` | buffered channel, 크기 = poolCount + 10 |
| 선제적 보충 | `Control.GetWorkConn()` | 풀에서 꺼낸 직후 `ReqWorkConn` 전송 |
| HTTP Transport 재사용 | `HTTPReverseProxy` | `MaxIdleConnsPerHost: 5`, `IdleConnTimeout: 60s` |

### 버퍼 관리

| 패턴 | 위치 | 설명 |
|------|------|------|
| 32KB BufferPool | `httputil.ReverseProxy.BufferPool` | sync.Pool 기반, GC 압력 감소 |
| 압축 풀링 | `WithCompressionFromPool` | zlib writer/reader 재사용 (장기 커넥션용) |

### 고루틴 관리

| 패턴 | 위치 | 설명 |
|------|------|------|
| 동기 메시지 처리 | Dispatcher readLoop | 핸들러를 읽기 루프에서 직접 실행 (고루틴 폭탄 방지) |
| 비동기 선택적 | `AsyncHandler` | NAT hole 등 시간 걸리는 것만 별도 고루틴 |
| 디스패처 쓰기 버퍼 | `sendCh` 크기 100 | 제어 채널 쓰기 경합 방지 |

### 타임아웃/Keepalive

| 설정 | 값 | 목적 |
|------|-----|------|
| 최초 메시지 읽기 | 10초 | 연결만 하고 데이터 안 보내는 공격 방어 |
| VHost 읽기/쓰기 | 30초 | HTTPS muxer 타임아웃 |
| HTTP ReadHeaderTimeout | 60초 | 느린 클라이언트 방어 |
| HTTP ResponseHeaderTimeout | 60초 (설정 가능) | 느린 백엔드 → 504 |
| 하트비트 체크 | 1초 간격 | 죽은 연결 빠르게 감지 |
| yamux MaxStreamWindowSize | 6MB | 높은 처리량 |
| TCP KeepAlive | 30초 | 끊어진 연결 감지 |

### 동시성 패턴

| 패턴 | 위치 | 설명 |
|------|------|------|
| `sync.RWMutex` | ControlManager, ProxyManager, Routers | 읽기 우세 접근에 최적화 |
| `atomic.Value` | `lastPing` | 락 없는 Ping 시간 갱신 |
| `atomic.AddUint64` | HTTP Group index | 락 없는 round-robin |
| `sync.Once` | Listener close | 중복 Close 방지 |
| Buffered channel | `workConnCh`, `sendCh` | 비동기 데이터 전달 |

## drps에 가져올 것 / 생략할 것

### 필수 (frpc 호환)

- yamux 멀티플렉싱 (MaxStreamWindowSize 6MB)
- 메시지 프로토콜 (Login, NewProxy, ReqWorkConn 등)
- 워크 커넥션 풀 + 선제적 보충
- 제어 채널 AES 암호화
- VHost 라우팅 (domain + location, 와일드카드)

### 권장 (성능)

- sync.Pool BufferPool (32KB)
- HTTP Transport 커넥션 재사용
- 동기 메시지 핸들러 (고루틴 폭탄 방지)
- 디스패처 쓰기 버퍼 (채널 크기 100)

### 생략 (HTTP 전용이므로)

- HTTPS SNI 파싱 / TLS 처리
- TCP/UDP/KCP/QUIC/SOCKS5 프록시
- HTTP Group 로드밸런싱 (Phase 1에서 불필요)
- h2c (HTTP/2 cleartext) 지원
- 서버 플러그인 시스템
- Prometheus 메트릭
- Visitor 기능 (P2P)
- Dashboard/API 서버
