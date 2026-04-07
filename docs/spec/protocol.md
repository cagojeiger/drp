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

### IV 전송

```
Writer: 첫 Write 시 랜덤 IV(16바이트) 선행 전송
Reader: 첫 Read 시 IV(16바이트) 선행 수신
```

Reader/Writer 독립 IV (양방향 각각).

### 사용 위치

| 위치 | 적용 시점 |
|------|----------|
| 제어 채널 | Login 성공 후 (항상) |
| 워크 커넥션 | UseEncryption=true 시 (선택) |

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
