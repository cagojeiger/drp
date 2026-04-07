# 컴포넌트 상세

## 1. config

설정 관리. 환경변수 → 플래그 → 기본값 우선순위.

```go
type Config struct {
    Token        string // 인증 토큰 (필수)
    FrpcAddr     string // frpc 리스너 주소 (기본: ":17000")
    HTTPAddr     string // HTTP 리스너 주소 (기본: ":18080")
    PoolCapacity int    // 워크 커넥션 풀 크기 (기본: 64)
}
```

환경변수: `DRPS_TOKEN`, `DRPS_FRPC_ADDR`, `DRPS_HTTP_ADDR`, `DRPS_POOL_CAPACITY`

---

## 2. server (Protocol Layer)

frpc와의 TCP+yamux 연결을 관리한다.

### Handler

```go
type Handler struct {
    Token            string
    ReadTimeout      time.Duration
    HeartbeatTimeout time.Duration
    Router           *router.Router
    OnWorkConn       func(conn net.Conn, m *msg.NewWorkConn)
    OnControlClose   func(runID string)
}
```

**HandleConnection(conn)**: 첫 메시지를 읽고 분기
- `Login` → 인증 → LoginResp → AES 래핑 → controlLoop
- `NewWorkConn` → OnWorkConn 콜백 (풀에 넣기)
- 기타 → 연결 닫기

**controlLoop**: 암호화된 제어 채널에서 메시지 수신
- `Ping` → Pong + heartbeat 갱신
- `NewProxy` → 라우터에 등록
- `CloseProxy` → 라우터에서 제거
- 연결 종료 시 → 모든 라우트 정리 + OnControlClose

### controlManager

RunID별로 active Control을 추적. 같은 RunID로 재연결 시 old Control을 cancel.

내부 구조: 단일 `controlEntry` 구조체로 cancel과 writer를 함께 관리.

```go
type controlEntry struct {
    cancel context.CancelFunc
    writer io.Writer
}

type controlManager struct {
    mu       sync.Mutex
    entries  map[string]*controlEntry
}
```

### ReqWorkConnFunc(runID)

제어 채널의 암호화된 writer를 통해 ReqWorkConn을 보내는 함수를 반환. Pool의 eager refill에 사용.

### registeredProxies

`map[string]struct{}` (O(1) 검색/삭제). controlLoop에서 등록된 프록시명 추적.

---

## 3. proxy (Service Layer)

HTTP 요청을 받아서 frpc로 전달한다.

### Handler

```go
type Handler struct {
    router          *router.Router
    poolLookup      PoolLookup   // func(runID string) → (*pool.Pool, bool)
    token           string
    aesKey          []byte       // 캐시된 AES 키 (서버 시작 시 1회 계산)
    WorkConnTimeout time.Duration
    ResponseTimeout time.Duration
}
```

**PoolLookup 시그니처 변경**: `proxyName` → `runID` 기반 조회로 변경. Router.Lookup 결과의 `cfg.RunID`를 직접 사용하여 이중 조회 제거.

**ServeHTTP(w, r)**:
1. Router.Lookup(Host, Path) → RouteConfig
2. Basic Auth 검증 (HTTPUser 설정 시)
3. poolLookup(cfg.RunID) → Pool 획득 (RangeByProxy 순회 제거)
4. Pool.Get() → 워크 커넥션 획득
5. wrap.Wrap(conn, aesKey, ...) → StartWorkConn + 암호화/압축 (캐시된 키 사용)
6. Custom Headers 주입 + HostHeaderRewrite 적용
7. req.Write → http.ReadResponse → 응답 전달
8. 101 Switching Protocols → handleUpgrade (양방향 relay)

### connTransport

하나의 워크 커넥션 위에서 HTTP 요청/응답을 전달하는 RoundTripper.
`bufio.Reader`를 sync.Pool에서 가져와 재사용.

### handleUpgrade (WebSocket)

양방향 io.Copy relay. 양쪽 goroutine 모두 완료 대기 후 정리.

---

## 4. router (Bridge Layer)

도메인+경로 → RouteConfig 매핑.

```go
type RouteConfig struct {
    Domain, Location, ProxyName, RunID string
    UseEncryption, UseCompression      bool
    HTTPUser, HTTPPwd                  string
    HostHeaderRewrite                  string
    Headers, ResponseHeaders           map[string]string
}
```

**라우팅 우선순위**:
1. 정확한 도메인 > 와일드카드 (`*.example.com`)
2. longest prefix match (`/api/v2` > `/api` > `/`)

**Remove(proxyName)**: 해당 프록시의 모든 도메인/경로를 일괄 제거.

**RangeByProxy 제거**: 더 이상 proxyName→runID 역방향 조회 불필요. Lookup 결과에 RunID가 이미 포함.

---

## 5. pool

워크 커넥션 풀. 채널 기반 (설정 가능한 capacity).

```go
func New(requestFn func(), capacity int) *Pool
```

- **Get(timeout)**: 즉시 시도 → 없으면 requestFn 호출 → 대기 → 타임아웃
- **Put(conn)**: 풀에 넣기 (가득 차면 버림)
- **eager refill**: Get 성공 시 비동기로 requestFn 호출 (ReqWorkConn 전송)

### Registry

RunID → Pool 매핑. GetOrCreate로 lazy 생성.

---

## 6. wrap

워크 커넥션을 사용 준비 상태로 만든다.

**Wrap(conn, aesKey, proxyName, enc, comp)**:
1. StartWorkConn 메시지 전송 (항상 평문)
2. UseEncryption → AES-128-CFB 래핑 (전달받은 캐시 키 사용, DeriveKey 호출 없음)
3. UseCompression → snappy 래핑
4. io.ReadWriteCloser 반환

래핑 순서: `conn → [AES] → [snappy] → HTTP 바이트` (frpc와 동일)

---

## 7. msg

frp 와이어 프로토콜. `[1B type][8B length BE][JSON body]`

10개 메시지: Login, LoginResp, NewProxy, NewProxyResp, CloseProxy, ReqWorkConn, NewWorkConn, StartWorkConn, Ping, Pong

frp v0.68.0 필드 완전 일치. MaxBodySize = 10240.

### 성능 개선

- **WriteMsg**: type + length + body를 단일 버퍼에 조립 → 1회 Write (기존 3회 → 1회 syscall)
- **TypeOf**: `fmt.Sprintf("%T")` 대신 switch type assertion (할당 없음)
- **ReadMsg**: 헤더 버퍼(9바이트) 스택 할당, body는 sync.Pool 재사용

---

## 8. auth

`MD5(token + timestamp)` 인증. constant-time 비교.

---

## 9. crypto

- **DeriveKey(token)**: PBKDF2(token, salt="frp", iter=64, keyLen=16, sha1)
- **NewCryptoWriter/Reader**: AES-128-CFB, 랜덤 IV 선행 전송
- **NewSnappyWriter/Reader**: snappy 압축, Write마다 자동 Flush

DeriveKey는 서버 시작 시 1회만 호출. 결과를 proxy.Handler.aesKey에 캐시.

## 외부 의존성

| 라이브러리 | 용도 |
|-----------|------|
| `hashicorp/yamux` | TCP 멀티플렉싱 |
| `golang/snappy` | 압축 |
| `x/crypto` | PBKDF2 키 파생 |
