# drp Failure Test Matrix — 장애 테스트 매트릭스

## 현재 상태

drp에 이미 장애 시 정리 로직이 구현되어 있으나, 해당 로직을 검증하는 테스트가 부족하다.

### Layer별 장애 처리 현황

| Layer | 파일 | 끊김 시 동작 | 복구 방식 | 테스트 |
|-------|------|------------|----------|--------|
| Control conn (서버) | `server_control.go:95-101` | defer: services 삭제 + UnregisterService + Close | 없음 (클라이언트 재접속) | 부분적 |
| Client controlLoop | `client.go:131-138` | EOF → ctx.Err() 확인 → 에러 반환 | 없음 (외부 재시작) | 미검증 |
| Client heartbeat | `client.go:120-124` | Write 실패 → conn.Close() | 없음 | 미검증 |
| Work conn broker | `broker.go:39-44` | 10초 timeout → ErrWorkConnTimeout | 504 응답 | 검증됨 |
| QUIC relay cache | `quic.go:218-228` | OpenStreamSync 실패 → 캐시 삭제 → 재다이얼 | **자동 복구** | 미검증 |
| Mesh NotifyLeave | `delegate.go:148-154` | RemoveByNode + RemoveNodeMeta | SWIM 자동 감지 | 검증됨 |
| Protocol framing | `framing.go:17-21` | protodelim EOF → 에러 반환 | 호출자 결정 | 미검증 |

---

## 전체 장애 분류 (문헌 교차 검증)

| ID | 장애 카테고리 | DDIA | Tanenbaum | Jepsen | Netflix | SRE | drp 관련 |
|----|-------------|------|-----------|--------|---------|-----|---------|
| N1 | 완전한 연결 끊김 | O | Crash | `:partition` | Chaos Monkey | O | SWIM, QUIC |
| N2 | 비대칭 파티션 (A→B OK, B→A 실패) | O | Omission | `:partition :one` | - | - | SWIM |
| N3 | 패킷 손실 (확률적) | O | Omission | `:packet :loss` | Latency Monkey | O | QUIC |
| N4 | 패킷 지연 / 높은 latency | O | Timing | `:packet :delay` | Latency Monkey | O | SWIM |
| N5 | 패킷 재정렬 | O | - | `:packet :reorder` | - | - | QUIC |
| N6 | 패킷 중복 | O | - | `:packet :duplicate` | - | - | 멱등성 |
| N7 | 패킷 변조 (bit flip) | O | - | `:packet :corrupt` | - | - | protobuf |
| N8 | 대역폭 제한 | O | Timing | `:packet :rate` | - | O | - |
| N9 | Split-brain (과반수 파티션) | O | - | `:partition :majority` | Chaos Gorilla | O | SWIM |
| P1 | 프로세스 크래시 (SIGKILL) | O | Crash-stop | `:kill` | Chaos Monkey | O | 멤버십 |
| P2 | 프로세스 일시정지 (SIGSTOP) | O | - | `:pause` | - | - | SWIM FP |
| P3 | 크래시 후 복구 (재시작) | O | Crash-recovery | `:kill`+`:start` | - | O | 재합류 |
| C1 | 시계 앞으로 점프 | O | Timing | `:clock :bump` | - | - | timeout |
| C2 | 시계 뒤로 점프 | O | Timing | `:clock :bump` | - | - | timeout |
| X1 | 일시정지 + 지연 (복합) | O | - | compose | - | - | NATS 패턴 |

---

## 유닛 테스트 가능 여부

| 장애 | 유닛 테스트 | 통합 테스트 | 이유 |
|------|-----------|-----------|------|
| N1 완전 끊김 | **O** `conn.Close()` → `io.EOF` | O `iptables` | 둘 다 필요: 로직 + 타이밍 |
| N2 비대칭 파티션 | **O** 읽기만 되는 mock conn | O `iptables` per-direction | 비대칭 mock 어려움 |
| N3 패킷 손실 | **O** 확률적 Write drop | O `tc netem loss` | TCP 재전송 못 봄 |
| N4 패킷 지연 | **O** `slowConn` | O `tc netem delay` | timeout cascade 못 봄 |
| N7 패킷 변조 | **O** 바이트 오염 mock | O `tc netem corrupt` | checksum 검증 |
| N9 Split-brain | **O** 멤버십 mock | O `iptables` 그룹 | SWIM 수렴 로직 |
| P1 프로세스 크래시 | **O** 모든 conn Close | O `kill -9` | 재연결 로직 |
| P2 프로세스 일시정지 | **O** 핸들러에 `time.Sleep` | O `kill -STOP` | timeout 감지 |

---

## 구현할 테스트 목록

### Tier 1 — 즉시 구현 (OSDI 2014: 장애 92%의 원인)

| ID | 테스트 | 시뮬레이션 방법 | 검증 대상 | 파일 |
|----|--------|---------------|----------|------|
| T1 | Control conn EOF mid-session | `pipe.Close()` during clientSession | defer가 services 삭제 + unregister | `unit_test.go` |
| T2 | Client controlLoop EOF | 서버 쪽 pipe 닫기 | `Run()` 에러 반환, goroutine 정리 | `client_test.go` |
| T3 | Client heartbeat write fail | `faultConn.InjectWriteError()` | conn.Close() 호출 | `client_test.go` |
| T4 | QUIC cache evict + redial | 첫 성공 → 에러 주입 → 두번째 호출 | 캐시 삭제 + 새 연결 | `quic_test.go` |
| T5 | ReadEnvelope partial/corrupt | 불완전 바이트 + Close() | 에러 반환, panic 없음 | `framing_test.go` |
| T6 | Pipe 중간 끊김 | 양방향 복사 중 한 쪽 Close() | 반대편도 정리 | `framing_test.go` |

### Tier 2 — 후속 구현

| ID | 테스트 | 시뮬레이션 방법 | 검증 대상 |
|----|--------|---------------|----------|
| T7 | 동시 다중 work conn 요청 | 여러 goroutine RequestAndWait | race 없음 |
| T8 | Work conn queue 포화 | 10+ conn 큐잉 | 11번째 동작 |
| T9 | 서버 shutdown 중 요청 | ctx cancel 후 handleHTTP | graceful |
| T10 | 동일 hostname 중복 등록 | 두 클라이언트 동시 등록 | 충돌 처리 |

### Tier 3 — 엣지 케이스

| ID | 테스트 | 시뮬레이션 방법 | 검증 대상 |
|----|--------|---------------|----------|
| T11 | 빈 alias/hostname NewProxy | 빈 문자열 전송 | 거부 |
| T12 | QUIC stream 즉시 close | OpenStreamSync → Close | 정리 정상 |
| T13 | 잘못된 Envelope 타입 | 서버에 Pong 전송 | 에러 로그, panic 없음 |

---

## 필요한 인프라 코드

### 1. faultConn (에러 주입 래퍼)

`internal/server/fake_test.go` 또는 별도 `testutil` 패키지에 추가.

→ [go-testing-patterns.md](./go-testing-patterns.md) 의 faultConn 섹션 참조.

### 2. faultDialer (다이얼 시 에러 주입)

```go
type faultDialer struct {
    conn net.Conn
    err  error
}

func (d *faultDialer) Dial(addr string) (net.Conn, error) {
    return d.conn, d.err
}
```

→ Client 테스트에서 `transport.Dialer` 인터페이스 만족.

---

## 검증 기준

각 테스트는 다음을 확인해야 한다:

1. **정리(cleanup)**: 리소스가 해제되는가? (conn close, map 삭제, unregister)
2. **전파 차단**: 에러가 다른 세션에 영향을 주지 않는가?
3. **응답**: 유저에게 올바른 HTTP 에러 코드 반환하는가? (502, 504)
4. **goroutine**: 누수 없는가? (done 채널로 확인)
5. **race**: `-race` 플래그에서 통과하는가?
