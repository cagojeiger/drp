# 컴포넌트 상세

## 1. server (Protocol Layer)

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

### ReqWorkConnFunc(runID)

제어 채널의 암호화된 writer를 통해 ReqWorkConn을 보내는 함수를 반환. Pool의 eager refill에 사용.

---

## 2. proxy (Service Layer)

HTTP 요청을 받아서 frpc로 전달한다.

### Handler

```go
type Handler struct {
    router          *router.Router
    poolLookup      PoolLookup
    token           string
    WorkConnTimeout time.Duration
    ResponseTimeout time.Duration
}
```

**ServeHTTP(w, r)**: 
1. Router.Lookup(Host, Path) → RouteConfig
2. Basic Auth 검증 (HTTPUser 설정 시)
3. Pool.Get() → 워크 커넥션 획득
4. wrap.Wrap() → StartWorkConn + 암호화/압축
5. Custom Headers 주입
6. HostHeaderRewrite 적용
7. req.Write → http.ReadResponse → 응답 전달
8. 101 Switching Protocols → handleUpgrade (양방향 relay)

### connTransport

하나의 워크 커넥션 위에서 HTTP 요청/응답을 전달하는 RoundTripper.

---

## 3. router (Bridge Layer)

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

---

## 4. pool

워크 커넥션 풀. 채널 기반 (버퍼 64).

- **Get(timeout)**: 즉시 시도 → 없으면 requestFn 호출 → 대기 → 타임아웃
- **Put(conn)**: 풀에 넣기 (가득 차면 버림)
- **eager refill**: Get 성공 시 비동기로 requestFn 호출 (ReqWorkConn 전송)

### Registry

RunID → Pool 매핑. GetOrCreate로 lazy 생성.

---

## 5. wrap

워크 커넥션을 사용 준비 상태로 만든다.

**Wrap(conn, token, proxyName, enc, comp)**:
1. StartWorkConn 메시지 전송 (항상 평문)
2. UseEncryption → AES-128-CFB 래핑
3. UseCompression → snappy 래핑
4. io.ReadWriteCloser 반환

래핑 순서: `conn → [AES] → [snappy] → HTTP 바이트` (frpc와 동일)

---

## 6. msg

frp 와이어 프로토콜. `[1B type][8B length BE][JSON body]`

10개 메시지: Login, LoginResp, NewProxy, NewProxyResp, CloseProxy, ReqWorkConn, NewWorkConn, StartWorkConn, Ping, Pong

frp v0.68.0 필드 완전 일치. MaxBodySize = 10240.

---

## 7. auth

`MD5(token + timestamp)` 인증. constant-time 비교.

---

## 8. crypto

- **DeriveKey(token)**: PBKDF2(token, salt="frp", iter=64, keyLen=16, sha1)
- **NewCryptoWriter/Reader**: AES-128-CFB, 랜덤 IV 선행 전송
- **NewSnappyWriter/Reader**: snappy 압축, Write마다 자동 Flush

## 외부 의존성

| 라이브러리 | 용도 |
|-----------|------|
| `hashicorp/yamux` | TCP 멀티플렉싱 |
| `golang/snappy` | 압축 |
| `x/crypto` | PBKDF2 키 파생 |
