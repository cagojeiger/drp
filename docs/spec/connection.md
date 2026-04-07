# 연결 스펙

## TCP + yamux

```
frpc → TCP :frpcAddr → drps
drps: yamux.Server(conn, cfg) → 세션 생성
```

하나의 TCP 연결 위에 N개의 논리 스트림.

## 연결 생명주기

### 1. Login (스트림 #1)

```
frpc → Login (평문) {version, user, privilege_key, timestamp, run_id, pool_count}
drps → 인증 검증
drps → LoginResp (평문) {version, run_id}
drps → AES 래핑 시작 (캐시된 키 사용, 이후 모든 제어 메시지 암호화)
drps → ReqWorkConn × PoolCount
```

- Login 자체는 평문. LoginResp 이후부터 암호화.
- run_id가 빈 문자열이면 drps가 새로 생성.

### 2. 프록시 등록 (암호화)

```
frpc → NewProxy {proxy_name, proxy_type:"http", custom_domains, ...}
drps → 라우팅 테이블에 도메인 등록
drps → NewProxyResp {proxy_name, remote_addr:":80"}
```

- `proxy_type != "http"` → 거부
- 도메인 중복 → 거부
- 여러 도메인 동시 등록 가능 (custom_domains 배열)

### 3. 워크 커넥션 (스트림 #2~)

```
frpc → 새 yamux 스트림 → NewWorkConn (평문) {run_id}
drps → 풀에 저장
```

handleConnection이 메시지 하나 읽고 → 소유권 이전 → 고루틴 종료.

### 4. Heartbeat (암호화)

```
frpc → Ping
drps → Pong
```

drps가 HeartbeatTimeout 동안 Ping을 못 받으면 연결 종료 (좀비 방지).

## 재연결

```
frpc → Login (같은 RunID)
drps → 기존 Control 찾기 → context cancel → old 종료
drps → old 라우트 전부 제거 + 풀 닫기
drps → 새 Control 생성
frpc → NewProxy 재등록
```

old cleanup이 완료된 후 new 등록이 시작됨 (context cancel → conn.Close → controlLoop 종료).

## 연결 해제

```
frpc 끊김 → drps:
  1. controlLoop 종료 감지 (ReadMsg 에러)
  2. 해당 frpc의 모든 라우트 제거 (Router.Remove)
  3. OnControlClose 호출 → 풀 닫기 (Registry.Remove)
  4. controlManager에서 제거
```

### 구현

`internal/server` — Handler, controlManager, controlLoop
