# 프로토콜 스펙

frpc v0.68.0 호환. frp 패키지 import 없이 직접 구현.

## 와이어 포맷

```
[1 byte: 타입코드] [8 bytes: int64 BE 길이] [N bytes: JSON 본문]
```

최대 본문 크기: 10240 bytes.

### 타입코드

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

### 구현

`internal/msg` — ReadMsg, WriteMsg, 메시지 구조체 10개 (frp v0.68.0 필드 완전 일치)

### 성능 설계

**WriteMsg 버퍼링**: type(1B) + length(8B) + body(NB)를 단일 `[]byte`에 조립 후 1회 `Write` 호출. 기존 3회 개별 Write → 3 syscall 문제 해결.

```
기존: w.Write(type) → binary.Write(length) → w.Write(body)  // 3 syscall
개선: buf = [type | length | body] → w.Write(buf)            // 1 syscall
```

**TypeOf 리플렉션 제거**: `fmt.Sprintf("%T", m)` 문자열 할당 대신 switch type assertion으로 타입 바이트 반환.

```go
// 기존: fmt.Sprintf("%T", m) → map lookup (매번 문자열 할당)
// 개선: switch m.(type) → 직접 반환 (할당 없음)
func TypeOf(m Message) (byte, bool) {
    switch m.(type) {
    case *Login:        return 'o', true
    case *LoginResp:    return '1', true
    ...
    }
}
```

**ReadMsg 버퍼 재사용**: 헤더 9바이트는 스택 할당 `[9]byte`. body 버퍼는 sync.Pool에서 가져와 재사용.

## 인증

```
privilege_key = MD5(token + timestamp)
```

- Login 시 frpc가 `privilege_key` + `timestamp` 전송
- drps가 `MD5(server_token + timestamp)` 계산 후 constant-time 비교
- 인증 실패 → LoginResp.Error + 연결 종료

### 구현

`internal/auth` — BuildAuthKey, VerifyAuth (`crypto/md5` + `crypto/subtle`)

## 암호화 (AES-128-CFB)

### 키 파생

```
key = PBKDF2(token, salt="frp", iter=64, keyLen=16, sha1)
```

- frp의 golib 소스와 파라미터 1:1 대조 완료
- `DefaultSalt = "crypto"` → frp가 init()에서 `"frp"`로 덮어씀
- **서버 시작 시 1회만 계산**, 이후 캐시된 키를 wrap/server에 전달

### IV 전송

```
Writer: 첫 Write 시 랜덤 IV(16바이트) 선행 전송
Reader: 첫 Read 시 IV(16바이트) 선행 수신
```

Reader/Writer 독립 IV (양방향 각각).

### 사용 위치

| 위치 | 적용 시점 | 키 소스 |
|------|----------|---------|
| 제어 채널 | Login 성공 후 (항상) | 캐시된 키 |
| 워크 커넥션 | UseEncryption=true 시 (선택) | 캐시된 키 |

## 압축 (snappy)

- frpc의 `UseCompression=true` 시 적용
- 동일 라이브러리 (`github.com/golang/snappy`)
- Write마다 자동 Flush (양방향 통신 데드락 방지)

### 래핑 순서

```
conn → [AES-128-CFB] → [snappy] → HTTP 바이트
```

drps와 frpc 양쪽 동일.

### 구현

`internal/crypto` — DeriveKey, NewCryptoWriter/Reader, NewSnappyWriter/Reader
