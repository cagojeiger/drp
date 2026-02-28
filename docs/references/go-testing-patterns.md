# Go Testing Patterns — 장애 시뮬레이션 구현 패턴

## 1. net.Pipe() 기본 동작

**Go 1.12+** 기준. 동기(synchronous) 연결 — 쓰기는 상대방이 읽을 때까지 블로킹.

### 에러 시맨틱

```go
c1, c2 := net.Pipe()
c1.Close()

// 닫은 쪽 (c1):
c1.Read(nil)   → io.ErrClosedPipe   // 본인이 닫은 conn
c1.Write(nil)  → io.ErrClosedPipe

// 상대쪽 (c2):
c2.Read(nil)   → io.EOF             // 상대방이 닫힘 = EOF
c2.Write(nil)  → io.ErrClosedPipe   // 닫힌 상대에 쓰기 불가
```

### Deadline (Go 1.12+ 완전 지원)

```go
c1.SetReadDeadline(time.Now().Add(-1))
c1.Read(buf)  → os.ErrDeadlineExceeded   // *net.OpError로 래핑됨

// 확인 방법:
errors.Is(err, os.ErrDeadlineExceeded)
// 또는
var netErr net.Error
errors.As(err, &netErr) && netErr.Timeout()
```

### Half-close

`net.Pipe()`는 **half-close를 지원하지 않는다** (CloseWrite/CloseRead 없음).
half-close가 필요하면 커스텀 래퍼 필요.

### 소스 링크

- pipe.go: https://github.com/golang/go/blob/011e98af/src/net/pipe.go
- pipe_test.go: https://github.com/golang/go/blob/011e98af/src/net/pipe_test.go

---

## 2. faultConn — 에러 주입 래퍼

### 기본 패턴

```go
type faultConn struct {
    net.Conn
    readErr  atomic.Pointer[error]
    writeErr atomic.Pointer[error]
}

func (c *faultConn) Read(b []byte) (int, error) {
    if p := c.readErr.Load(); p != nil {
        return 0, *p
    }
    return c.Conn.Read(b)
}

func (c *faultConn) Write(b []byte) (int, error) {
    if p := c.writeErr.Load(); p != nil {
        return 0, *p
    }
    return c.Conn.Write(b)
}

func (c *faultConn) InjectReadError(err error)  { c.readErr.Store(&err) }
func (c *faultConn) InjectWriteError(err error) { c.writeErr.Store(&err) }
func (c *faultConn) ClearFaults()               { c.readErr.Store(nil); c.writeErr.Store(nil) }
```

### 변형 — slowConn (지연 주입)

```go
type slowConn struct {
    net.Conn
    delay time.Duration
}

func (c *slowConn) Read(b []byte) (int, error) {
    time.Sleep(c.delay)
    return c.Conn.Read(b)
}
```

### 변형 — partialConn (부분 쓰기)

```go
type partialConn struct {
    net.Conn
    maxWrite int
}

func (c *partialConn) Write(b []byte) (int, error) {
    if len(b) > c.maxWrite {
        b = b[:c.maxWrite]
    }
    return c.Conn.Write(b)
}
```

---

## 3. HashiCorp 패턴

### hashicorp/memberlist — MockTransport

memberlist의 공식 테스트 패턴. **`net.Pipe()`로 스트림 연결을 시뮬레이션**.

```go
// mock_transport.go
func (t *MockTransport) DialAddressTimeout(a Address, timeout time.Duration) (net.Conn, error) {
    dest, ok := t.net.transportsByAddr[a.Addr]
    if !ok {
        return nil, fmt.Errorf("no route to %s", a)  // 파티션 시뮬레이션
    }
    c1, c2 := net.Pipe()
    dest.streamCh <- c1    // 서버 쪽
    return c2, nil          // 클라이언트 쪽
}
```

파티션 = 피어 맵에서 삭제.

### hashicorp/memberlist — errorReadNetConn

에러 주입 + 정리 확인용 래퍼.

```go
// net_test.go
type errorReadNetConn struct {
    net.Conn
    closed chan struct{}
}

func (c *errorReadNetConn) Read(b []byte) (int, error) {
    return 0, fmt.Errorf("test read error")  // 항상 실패
}

func (c *errorReadNetConn) Close() error {
    close(c.closed)  // 테스트에서 정리 여부 확인
    return nil
}
```

### hashicorp/raft — InmemTransport

네트워크 파티션의 산업 표준 패턴.

```go
// inmem_transport.go
func (i *InmemTransport) Connect(peer ServerAddress, t Transport) {
    i.peers[peer] = t.(*InmemTransport)  // 연결
}

func (i *InmemTransport) Disconnect(peer ServerAddress) {
    delete(i.peers, peer)  // 파티션
}

func (i *InmemTransport) DisconnectAll() {
    i.peers = make(map[ServerAddress]*InmemTransport)  // 완전 격리
}
```

클러스터 파티션 테스트:

```go
// testing.go
func (c *cluster) Partition(far []ServerAddress) {
    // near 그룹과 far 그룹 사이 Disconnect
    for _, t := range c.trans {
        if isNear(t) {
            for _, a := range far { t.Disconnect(a) }
        } else {
            for a := range near { t.Disconnect(a) }
        }
    }
}
```

### 소스 링크

- memberlist MockTransport: https://github.com/hashicorp/memberlist/blob/313d20cc/mock_transport.go
- memberlist net_test.go: https://github.com/hashicorp/memberlist/blob/313d20cc/net_test.go
- raft InmemTransport: https://github.com/hashicorp/raft/blob/9071aaf1/inmem_transport.go

---

## 4. quic-go 패턴

### mockPacketConn — 채널 기반 에러 주입

```go
// transport_test.go
type mockPacketConn struct {
    localAddr net.Addr
    readErrs  chan error
}

func (c *mockPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
    err, ok := <-c.readErrs
    if !ok {
        return 0, nil, net.ErrClosed
    }
    return 0, nil, err
}
```

### deadlineError — Temporary vs Fatal 구분

```go
// stream.go
type deadlineError struct{}

func (deadlineError) Temporary() bool { return true }   // 재시도 가능
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Unwrap() error   { return os.ErrDeadlineExceeded }
```

Transport에서 Temporary 여부로 분기:

```go
// transport.go
if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
    continue  // 재시도
}
t.close(err)  // fatal — 종료
```

### 소스 링크

- quic-go transport_test.go: https://github.com/quic-go/quic-go/blob/94a509f9/transport_test.go
- quic-go stream.go: https://github.com/quic-go/quic-go/blob/94a509f9/stream.go

---

## 5. golang.org/x/net/nettest — net.Conn 구현 검증

커스텀 `net.Conn` 래퍼를 만들면 이 도구로 계약 준수 검증:

```go
import "golang.org/x/net/nettest"

func TestFaultConn(t *testing.T) {
    nettest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
        p1, p2 := net.Pipe()
        c1 = newFaultConn(p1)
        c2 = newFaultConn(p2)
        stop = func() { c1.Close(); c2.Close() }
        return
    })
}
```

검증 항목: BasicIO, PingPong, RacyRead, RacyWrite, ReadTimeout, WriteTimeout,
PastTimeout, PresentTimeout, FutureTimeout, CloseTimeout, ConcurrentMethods.

### 소스 링크

- conntest.go: https://github.com/golang/net/blob/60b3f6f8/nettest/conntest.go

---

## 6. 시나리오별 패턴 매핑

| 장애 시나리오 | 구현 패턴 | 핵심 API |
|-------------|----------|---------|
| 연결 끊김 | `c.Close()` | 상대방: `io.EOF` (Read), `io.ErrClosedPipe` (Write) |
| 읽기 타임아웃 | `SetReadDeadline(과거)` | `os.ErrDeadlineExceeded` |
| 쓰기 타임아웃 | `SetWriteDeadline(과거)` | `os.ErrDeadlineExceeded` |
| 느린 리더 | `slowConn{delay: 50ms}` | Write 블로킹 → deadline 초과 |
| 부분 쓰기 | `partialConn{maxWrite: 5}` | 불완전 메시지 전달 |
| 네트워크 파티션 | 피어 맵 삭제 (raft 패턴) | `"no route to peer"` |
| Half-open | 한쪽만 Close() | `io.EOF` (Read), Write는 블로킹 |
| 간헐적 장애 | `atomic.Bool + faultConn` | 테스트 중 에러 on/off 토글 |
| Temporary error | `net.Error` 구현 | Transport가 재시도 vs 종료 구분 |
