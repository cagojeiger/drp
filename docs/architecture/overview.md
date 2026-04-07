# drps 아키텍처 개요

## drps란?

frps(frp server)의 HTTP 전용 대체. frpc를 수정 없이 사용하며, frp 패키지를 import하지 않고 프로토콜을 직접 구현한다.

## 전체 흐름

```
[클라이언트] → HTTP :httpAddr → [proxy] → [router] → [pool] → [wrap] → [frpc] → [백엔드]
                                                                          ↑
[frpc] → TCP :frpcAddr → [yamux] → [server/handle] → Login/NewProxy/WorkConn
```

## 컴포넌트 구조

```
cmd/drps/main.go          ← 진입점: 설정 로드 + TCP 리스너 + HTTP 서버 + 조립
│
├── internal/config/       ← 설정 관리
│   └── config.go          ← Config 구조체, 환경변수/플래그 파싱
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
│   ├── pool.go            ← Get, Put, Close (설정 가능한 capacity)
│   └── registry.go        ← RunID → Pool 매핑
│
├── internal/wrap/         ← 커넥션 래핑
│   └── wrap.go            ← StartWorkConn + AES + snappy (캐시된 키 사용)
│
├── internal/msg/          ← 와이어 프로토콜
│   └── msg.go             ← ReadMsg, WriteMsg (버퍼링된 단일 Write), 구조체 10개
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
  ├── config (독립)
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

## 성능 설계 원칙

1. **Hot Path 할당 최소화**: sync.Pool로 bufio.Reader, msg 버퍼 재사용
2. **중복 계산 제거**: PBKDF2 키 파생 1회, 이후 캐시 사용
3. **이중 조회 제거**: Router.Lookup → cfg.RunID → Registry.Get 직접 조회
4. **syscall 최소화**: msg.WriteMsg는 단일 버퍼에 조립 후 1회 Write
5. **리플렉션 제거**: 메시지 타입 판별에 fmt.Sprintf 대신 switch type assertion
