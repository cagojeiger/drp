# 연결 테스트

`internal/server` 대상. 26개.

## 연결 핸들링 (5)

첫 메시지 읽기 → 분기 (Login / NewWorkConn / 기타).

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-01 | TestHandleConnectionLogin | P0 | Login 성공 → LoginResp(Error="", RunID) |
| S-02 | TestHandleConnectionLoginWrongToken | P0 | 인증 실패 → 에러 + 연결 닫힘 |
| S-03 | TestHandleConnectionNewWorkConn | P0 | NewWorkConn → OnWorkConn 콜백 호출 |
| S-04 | TestHandleConnectionUnknownFirstMsg | P1 | Ping 등 예상외 메시지 → 연결 닫힘 |
| S-05 | TestHandleConnectionReadTimeout | P1 | 메시지 안 보내면 타임아웃 종료 |

## 제어 채널 (5)

Login 후 AES 래핑된 제어 메시지 처리.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-06 | TestControlReqWorkConn | P0 | Login 후 ReqWorkConn × PoolCount 전송 |
| S-07 | TestControlPingPong | P0 | Ping → Pong (암호화 경로) |
| S-08 | TestControlNewProxy | P0 | NewProxy → NewProxyResp(Error="") |
| S-09 | TestControlNewProxyRejectNonHTTP | P0 | type=tcp → 거부 |
| S-10 | TestControlYamuxFullFlow | P0 | yamux 위 Login + WorkConn 3개 + Ping |

## yamux 통합 (4)

net.Pipe 위 yamux 세션에서 동작 확인.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-16 | TestYamuxLoginSuccess | P0 | yamux 스트림으로 Login 성공 |
| S-17 | TestYamuxLoginFailure | P0 | yamux 스트림으로 인증 실패 |
| S-18 | TestYamuxMultipleStreams | P0 | Login 1개 + WorkConn 3개 동시 |
| S-19 | TestYamuxConcurrentLogins | P1 | 2개 동시 Login → 독립 RunID |

## 라우트 연동 (5)

server → router 통합.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-11 | TestRouterIntegrationNewProxy | P0 | NewProxy → Router.Lookup 성공 |
| S-12 | TestRouterIntegrationMultipleDomains | P0 | 여러 도메인 동시 등록 |
| S-13 | TestRouterIntegrationCloseProxy | P0 | CloseProxy → Router에서 제거 |
| S-14 | TestRouterIntegrationDisconnectCleanup | P0 | 끊김 → 전체 라우트 자동 정리 |
| S-15 | TestRouterIntegrationDuplicateDomain | P1 | 이미 등록된 도메인 → 거부 |

## 재연결 (2)

같은/다른 RunID 재연결 처리.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-20 | TestReconnectSameRunID | P0 | old Control cancel → 라우트 정리 → 재등록 |
| S-21 | TestReconnectDifferentRunID | P1 | 독립 Control 공존 |

## 정리 (2)

연결 종료 시 리소스 해제.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-22 | TestCleanupOnDisconnect | P0 | 끊김 → OnControlClose(runID) 호출 |
| S-23 | TestCleanupMultipleProxies | P0 | 프록시 여러 개 → 전부 정리 |

## Heartbeat (2)

좀비 Control 감지.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-24 | TestHeartbeatTimeout | P1 | Ping 없으면 HeartbeatTimeout 후 종료 |
| S-25 | TestHeartbeatKeptAlive | P1 | Ping 보내면 유지, 안 보내야 종료 |

## Eager Refill (1)

워크 커넥션 사용 후 자동 보충.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| S-26 | TestEagerRefill | P0 | Pool.Get 후 제어 채널로 ReqWorkConn 전송 |
