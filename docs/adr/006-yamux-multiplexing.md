# ADR-006: yamux Multiplexing

## 상태
Accepted

## 컨텍스트

drpc ↔ drps 간 연결에서 제어 메시지와 work connection을 동시에 처리해야 한다.

frp의 접근:
- 제어 연결 1개 + work connection N개를 별도 TCP로 수립
- `chan net.Conn` with `poolCount+10` buffer
- 기본 `poolCount=1` → 실질 동시 ~10-20 연결
- `MaxConnection` 제한 없음 → goroutine 무한 생성 가능

이 방식의 문제:
- 매 work connection마다 새 TCP 핸드셰이크 → 지연
- 연결 수 제한 없이 goroutine이 무한 생성될 수 있음
- NAT 환경에서 다수의 TCP 연결이 conntrack 테이블 압박

단일 TCP 연결 위에 다중 스트림을 멀티플렉싱하면 이 문제를 해결할 수 있다.

## 결정

**hashicorp/yamux를 사용하여 단일 TCP 연결 위에 제어 + work connection을 멀티플렉싱한다.**

### 연결 구조

```
drpc ──── [TCP (WireGuard)] ──── drps
              │
         yamux session
         ├── stream 0: 제어 채널 (Login, NewProxy, Heartbeat)
         ├── stream 1: work conn #1
         ├── stream 2: work conn #2
         └── stream N: work conn #N
```

### 동작 방식

1. drpc가 drps:7000에 TCP 연결 수립 (WireGuard 경유)
2. TCP 연결 위에 yamux session 생성
3. 첫 번째 stream = 제어 채널 (Login → NewProxy → Heartbeat 루프)
4. `ReqWorkConn` 수신 시 새 yamux stream 열어 work conn으로 사용
5. work conn은 `StartWorkConn` 수신 후 로컬 서비스와 relay

### frp와의 차이

| | frp | drp |
|---|---|---|
| 제어 연결 | 전용 TCP 1개 | yamux stream #0 |
| work conn | 별도 TCP 매번 수립 | yamux stream (즉시 생성) |
| yamux 사용처 | 선택적 (기본 off) | **필수** |
| yamux 라이브러리 | fatedier/yamux (포크) | **hashicorp/yamux (원본)** |
| MaxStreamWindowSize | 6MB (하드코딩) | 기본값 사용 |

### 연결당 자원

| 자원 | 크기 |
|------|------|
| Goroutine (3개) | ~24KB |
| net.Conn (2개) | ~2KB |
| yamux stream state | ~1KB |
| **합계** | **~27KB/연결** |

## 결과

### 장점
- TCP 핸드셰이크 1회로 다수 스트림 사용
- NAT conntrack 테이블 압박 최소화 (TCP 1개)
- yamux는 hashicorp에서 유지보수하는 검증된 라이브러리
- frp에서도 (포크 버전이지만) 사용하여 검증됨

### 단점
- yamux 자체의 프레이밍 오버헤드 (미미)
- 단일 TCP 연결 장애 시 모든 스트림 영향
- 디버깅 시 개별 스트림 추적이 raw TCP보다 복잡

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| 매번 TCP 연결 (frp 기본) | 핸드셰이크 지연, NAT conntrack 압박 |
| QUIC | UDP 기반이라 일부 네트워크에서 차단. 구현 복잡도 높음 |
| HTTP/2 | 프레이밍 오버헤드. 바이너리 릴레이에 부자연스러움 |
| fatedier/yamux (포크) | 하드코딩된 값이 있고, 원본 대비 유지보수 불투명 |

## 참고 자료
- [hashicorp/yamux](https://github.com/hashicorp/yamux)
- drp CONTEXT.md §2.5, §8
- [ADR-001](./001-minimalist-architecture.md)
