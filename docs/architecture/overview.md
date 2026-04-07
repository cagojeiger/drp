# drps 아키텍처 개요

## drps란?

frps(frp server)의 HTTP 전용 대체. frpc를 수정 없이 사용하며, frp 패키지를 import하지 않고 프로토콜을 직접 구현한다.

## 전체 흐름

```
[클라이언트] → HTTP :18080 → [proxy] → [router] → [pool] → [wrap] → [frpc] → [백엔드]
                                                                        ↑
[frpc] → TCP :17000 → [yamux] → [server/handle] → Login/NewProxy/WorkConn
```

## 컴포넌트 구조

```
cmd/drps/main.go          ← 진입점: TCP 리스너 + HTTP 서버 + 조립
│
├── internal/server/       ← Protocol Layer: frpc 연결 처리
│   └── handle.go          ← HandleConnection, controlLoop
│
├── internal/proxy/        ← Service Layer: HTTP 요청 처리
│   └── proxy.go           ← ServeHTTP, connTransport, handleUpgrade
│
├── internal/router/       ← Bridge Layer: 라우팅 테이블
│   └── router.go          ← Add, Remove, Lookup
│
├── internal/pool/         ← 워크 커넥션 관리
│   ├── pool.go            ← Get, Put, Close
│   └── registry.go        ← RunID → Pool 매핑
│
├── internal/wrap/         ← 커넥션 래핑
│   └── wrap.go            ← StartWorkConn + AES + snappy
│
├── internal/msg/          ← 와이어 프로토콜
│   └── msg.go             ← ReadMsg, WriteMsg, 구조체 10개
│
├── internal/auth/         ← 토큰 인증
│   └── auth.go            ← BuildAuthKey, VerifyAuth
│
└── internal/crypto/       ← 암호화 + 압축
    ├── crypto.go          ← DeriveKey, NewCryptoWriter/Reader
    └── snappy.go          ← NewSnappyWriter/Reader
```

## 의존성 방향

```
main.go
  ├── server  → msg, auth, crypto, router
  ├── proxy   → router, pool, wrap
  ├── wrap    → msg, crypto
  ├── pool    (독립)
  ├── router  (독립)
  ├── msg     (독립)
  ├── auth    (독립)
  └── crypto  (독립)
```

순환 의존성 없음. 각 패키지는 단일 책임.
