# 프로토콜 스펙

frpc v0.68.0 호환. frp 패키지 import 없이 직접 구현.

## 와이어 포맷

```mermaid
packet-beta
  0-7: "Type (1B)"
  8-71: "Length (8B, int64 BE)"
  72-95: "JSON body (N bytes, max 10240)"
```

- 최대 본문 크기: **10240 bytes**
- `length`는 int64 Big-Endian
- 음수 length → 거부 (보안)

## 메시지 타입 (10개)

```mermaid
flowchart LR
    subgraph control["제어 채널 (암호화)"]
        direction LR
        Login["'o' Login"] --> LoginResp["'1' LoginResp"]
        NewProxy["'p' NewProxy"] --> NewProxyResp["'2' NewProxyResp"]
        CloseProxy["'c' CloseProxy"]
        Ping["'h' Ping"] --> Pong["'4' Pong"]
        ReqWorkConn["'r' ReqWorkConn"]
    end

    subgraph work["워크 커넥션 (평문)"]
        direction LR
        NewWorkConn["'w' NewWorkConn"]
        StartWorkConn["'s' StartWorkConn"]
    end
```

| 바이트 | 이름 | 방향 | 용도 |
|--------|------|------|------|
| `'o'` | Login | frpc → drps | 로그인 요청 |
| `'1'` | LoginResp | drps → frpc | 로그인 응답 |
| `'p'` | NewProxy | frpc → drps | 프록시 등록 |
| `'2'` | NewProxyResp | drps → frpc | 등록 응답 |
| `'c'` | CloseProxy | frpc → drps | 프록시 해제 |
| `'r'` | ReqWorkConn | drps → frpc | 워크 커넥션 요청 |
| `'w'` | NewWorkConn | frpc → drps | 워크 커넥션 등록 |
| `'s'` | StartWorkConn | drps → frpc | 워크 커넥션 사용 시작 |
| `'h'` | Ping | frpc → drps | 하트비트 |
| `'4'` | Pong | drps → frpc | 하트비트 응답 |

구현: `internal/msg` — 10개 구조체 + ReadMsg/WriteMsg.

## 인증

### 키 생성

```mermaid
flowchart LR
    token[server token] --> concat
    ts[timestamp<br/>unix 초] --> concat[concat]
    concat --> md5[MD5]
    md5 --> key[privilege_key<br/>32B hex]
```

### 검증 순서

```mermaid
sequenceDiagram
    participant F as frpc
    participant S as drps

    F->>F: ts = now()
    F->>F: key = MD5(token + ts)
    F->>S: Login{PrivilegeKey: key, Timestamp: ts}
    S->>S: expected = MD5(server_token + ts)
    alt ConstantTimeCompare(key, expected)
        S-->>F: LoginResp{Error: ""}
        Note over S: AES 래핑 시작
    else
        S-->>F: LoginResp{Error: "auth failed"}
        S->>S: conn.Close
    end
```

구현: `internal/auth` — `crypto/md5` + `crypto/subtle.ConstantTimeCompare`.

## 암호화 (AES-128-CFB)

### 키 파생

```mermaid
flowchart LR
    token[token] --> pbkdf2
    salt["salt = 'frp'"] --> pbkdf2
    iter["iter = 64"] --> pbkdf2
    len["keyLen = 16"] --> pbkdf2
    hash["hash = sha1"] --> pbkdf2
    pbkdf2[PBKDF2] --> key[aesKey 16B]
    key -.->|서버 시작 시 1회| cache["proxy.Handler.aesKey<br/>(캐시)"]
```

`DefaultSalt = "crypto"`는 frp가 `init()`에서 `"frp"`로 덮어씀. drps는 처음부터 `"frp"` 사용.

### IV 전송 규칙

```mermaid
sequenceDiagram
    participant W as Writer
    participant R as Reader
    W->>W: 첫 Write 시 rand 16B IV 생성
    W->>R: IV (16B)
    W->>R: encrypted data...
    R->>R: 첫 Read 시 IV 16B 수신
    R->>R: cipher 초기화
    Note over W,R: Writer/Reader 독립 IV (양방향 각각)
```

### 적용 위치

| 위치 | 적용 시점 | 키 소스 |
|------|----------|---------|
| 제어 채널 | Login 성공 후 (항상) | `crypto.DeriveKey(token)` (로그인 시 계산) |
| 워크 커넥션 | `UseEncryption=true` (선택) | 서버 시작 시 계산된 캐시 키 (`proxy.Handler.aesKey`) |

## 압축 (snappy)

`UseCompression=true` 시 적용. 동일 라이브러리 (`github.com/golang/snappy`). **Write마다 자동 Flush** (양방향 통신 데드락 방지).

## 래핑 순서

drps ↔ frpc 양쪽 동일.

```mermaid
flowchart LR
    conn[raw conn] --> aes[AES-128-CFB]
    aes --> snappy[snappy]
    snappy --> http[HTTP 바이트]
```

제어 채널: `conn → AES → 제어 메시지`
워크 커넥션: `conn → StartWorkConn(평문) → [AES?] → [snappy?] → HTTP`

## 성능 설계

### WriteMsg 단일 버퍼

```mermaid
flowchart TB
    subgraph old["기존 (3 syscall)"]
        direction LR
        o1[w.Write type] --> o2[binary.Write length] --> o3[w.Write body]
    end
    subgraph new["개선 (1 syscall)"]
        direction LR
        n1["buf = [type | length | body]"] --> n2[w.Write buf]
    end
    old -.-> new
```

### TypeOf 리플렉션 제거

```go
// 기존: fmt.Sprintf("%T", m) → map lookup (문자열 할당)
// 개선: switch type assertion (0 allocs)
func TypeOf(m Message) (byte, bool) {
    switch m.(type) {
    case *Login:     return 'o', true
    case *LoginResp: return '1', true
    // ...
    }
}
```

### ReadMsg 버퍼 재사용

- 헤더 9바이트: 스택 할당 `[9]byte`
- Body 버퍼: `sync.Pool` 재사용

구현: `internal/msg`, `internal/auth`, `internal/crypto`
