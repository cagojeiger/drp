# ADR-004: 프로토콜과 메시지

## 상태
Accepted

## 컨텍스트

컴포넌트 간 통신 규약이 필요하다:

- 클라이언트 ↔ 서버 (제어 + 데이터)
- 서버 ↔ 서버 (QUIC relay)
- 서버 ↔ 서버 (SWIM+Gossip — memberlist가 처리)

### 이전 접근의 문제

TLV+JSON의 한계:

| 문제 | 설명 |
|------|------|
| 스키마 없음 | 필드 추가·제거 시 양쪽 코드 동시 수정 필요 |
| 타입 안전성 없음 | JSON 파싱 후 런타임에야 타입 불일치 발견 |
| 하위 호환성 | 보장 메커니즘 없음. 롤링 업데이트 불가 |
| 성능 | JSON은 바이너리 포맷 대비 크기 ~10배, 파싱 ~100배 느림 |

## 결정

**Protocol Buffers로 메시지를 직렬화한다.**

### 왜 Protocol Buffers인가

| 특성 | TLV+JSON | Protocol Buffers |
|------|----------|-----------------|
| 스키마 | 없음 (암묵적) | **.proto 파일** (명시적) |
| 타입 안전 | 런타임 | **컴파일 타임** |
| 하위 호환 | 수동 관리 | **필드 번호 기반 자동 호환** |
| 메시지 크기 | 기준 | **~10배 작음** |
| 파싱 속도 | 기준 | **~100배 빠름** |
| 언어 지원 | JSON 라이브러리 | **공식 코드 생성 (Go, Python, Java, ...)** |
| 디버깅 | JSON 평문 읽기 가능 | `protoc --decode`, `grpcurl` |

**핵심**: 제어 메시지 빈도가 낮아 성능은 부차적. **스키마 진화와 하위 호환성**이 Protobuf를 선택한 진짜 이유다.

### 메시지 정의

```protobuf
syntax = "proto3";
package drp.v1;

// ─── Envelope ──────────────────────────────────
// 모든 제어 메시지를 하나의 프레임으로 감싸는 컨테이너.
// oneof로 메시지 타입을 구분한다.

message Envelope {
    oneof payload {
        // 클라이언트 → 서버
        Login login = 1;
        NewProxy new_proxy = 2;
        NewWorkConn new_work_conn = 3;
        Ping ping = 4;

        // 서버 → 클라이언트
        LoginResp login_resp = 10;
        NewProxyResp new_proxy_resp = 11;
        ReqWorkConn req_work_conn = 12;
        StartWorkConn start_work_conn = 13;
        Pong pong = 14;

        // 서버 ↔ 서버 (QUIC relay)
        RelayOpen relay_open = 20;
    }
}

// ─── 클라이언트 → 서버 ──────────────────────────

message Login {
    string api_key = 1;
    string version = 2;
}

message NewProxy {
    string alias = 1;
    string hostname = 2;
    string type = 3;         // "http" | "https"
}

message NewWorkConn {
    string proxy_alias = 1;
}

message Ping {}

// ─── 서버 → 클라이언트 ──────────────────────────

message LoginResp {
    bool ok = 1;
    string error = 2;
}

message NewProxyResp {
    bool ok = 1;
    string error = 2;
}

message ReqWorkConn {
    string proxy_alias = 1;
}

message StartWorkConn {
    string proxy_alias = 1;
}

message Pong {}

// ─── 서버 ↔ 서버 (QUIC relay stream) ───────────

message RelayOpen {
    string proxy_alias = 1;
    string request_id = 2;
}

// ─── 서버 ↔ 서버 (memberlist gossip) ────────────
// memberlist Delegate를 통해 전파. Envelope과 별개 경로.

message ServiceUpdate {
    string node_id = 1;
    string action = 2;       // "add" | "remove"
    string proxy_alias = 3;
    string hostname = 4;
}
```

### 메시지 프레이밍

Protobuf는 자체 길이 구분이 없으므로 **length-prefix** 프레이밍을 사용:

```
┌──────────────────┬──────────────────────┐
│ Length (varint)   │ Envelope (protobuf)  │
│ 1~5 bytes        │ variable             │
└──────────────────┴──────────────────────┘
```

Go 공식 라이브러리 `google.golang.org/protobuf/encoding/protodelim`을 사용:

1. **쓰기**: `protodelim.MarshalTo(w, envelope)` — varint 길이 프리픽스 + protobuf 바이트
2. **읽기**: `protodelim.UnmarshalFrom(bufio.NewReader(r), envelope)` — varint 읽기 + unmarshal
3. `Envelope.payload` 의 oneof 분기로 메시지 타입 결정

기존 TLV에서 Type 바이트가 사라졌다 — oneof가 타입 역할을 대신한다.
protodelim은 Protobuf 생태계 표준이며, 추가 구현 없이 Go 공식 라이브러리가 처리한다.

### 제어 평면 vs 데이터 평면

```
제어 평면 (Protobuf):
    drpc ↔ drps: Login, NewProxy, Heartbeat, ReqWorkConn, ...
    drps ↔ drps: RelayOpen (QUIC stream 첫 메시지)

데이터 평면 (raw bytes):
    StartWorkConn 이후: 양쪽 연결을 pipe()로 연결. Protobuf 없음.
    QUIC relay: RelayOpen 이후: stream을 user conn과 pipe(). Protobuf 없음.
```

**전환 지점**: `StartWorkConn` 또는 `RelayOpen` 직후 — 이 시점부터 연결은 투명한 바이트 파이프가 된다.

### Gossip 메시지 경로

`ServiceUpdate`는 Envelope을 거치지 않는다. memberlist의 Delegate 인터페이스로 전파:

```
서비스 등록:
    drpc → drps-A: Envelope{NewProxy}        (TCP :9000)
    drps-A 내부: ServiceUpdate 생성
    drps-A → memberlist: Delegate.NotifyMsg   (UDP :7946, gossip)
    memberlist → 전체 클러스터: gossip 전파

서비스 검색:
    drps-B: 로컬 레지스트리 조회 (gossip으로 이미 수신한 ServiceUpdate)
```

두 가지 직렬화 경로가 존재하지만, 모두 Protobuf를 사용한다:

| 경로 | 전송 | 프레이밍 | 메시지 |
|------|------|---------|--------|
| 제어 연결 (drpc ↔ drps) | TCP :9000 | length-prefix + Envelope | Login, NewProxy, ... |
| QUIC relay (drps ↔ drps) | QUIC :9001 | length-prefix + Envelope | RelayOpen |
| Gossip (drps ↔ drps) | memberlist :7946 | memberlist 내부 프레이밍 | ServiceUpdate |

### 포트 설계

| 포트 | 용도 | 첫 메시지 | 접근 |
|------|------|----------|------|
| :80 | HTTP 사용자 트래픽 | — | 공개 |
| :443 | HTTPS 사용자 트래픽 (SNI) | — | 공개 |
| :9000 | drpc 제어 연결 | Envelope{Login} | LB 경유 |
| :9001 | QUIC relay | Envelope{RelayOpen} | 클러스터 내부 |
| :7946 | SWIM+Gossip (memberlist) | memberlist 프로토콜 | 클러스터 내부 |

:9000이 이전처럼 mesh와 client를 동시에 처리할 필요 없다 — mesh는 :7946(SWIM)과 :9001(relay)로 분리되었다.

### 보안

```
Layer 1: 전송 암호화
  ├─ drpc ↔ drps :9000 — TLS (선택)
  ├─ drps ↔ drps :9001 — QUIC 내장 TLS 1.3 (필수)
  ├─ :443 — SNI 패스스루 (end-to-end TLS 보존)
  └─ :7946 — memberlist encryption key (선택)

Layer 2: 인증
  └─ API Key (Login 메시지)

Layer 3: 인가
  └─ API Key별 허용 서비스 목록 (ACL)
```

### 타임아웃

| 항목 | 기본값 | 목적 |
|------|--------|------|
| Idle | 5분 | 유휴 제어 연결 정리 |
| Heartbeat 간격 | 30초 | 제어 연결 생존 확인 |
| Heartbeat 실패 | 90초 (3회) | 연결 종료 → 자동 재연결 |

## 결과

### 장점
- **스키마 진화**: 필드 번호 기반 하위 호환. 롤링 업데이트 가능
- **타입 안전**: 컴파일 타임 검증. 런타임 타입 에러 방지
- **멀티 언어**: .proto에서 Go, Python, Java 등 코드 자동 생성
- 포트 분리로 역할 명확화 (:9000은 drpc 전용)

### 단점
- protoc 코드 생성 빌드 단계 추가
- JSON 대비 디버깅 시 도구 필요 (`protoc --decode`)
- google/protobuf 라이브러리 의존

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| TLV+JSON (이전) | 스키마 없음, 하위 호환성 없음, 타입 안전 없음 |
| gRPC | HTTP/2 필수. 바이너리 relay 전환 부자연스러움 |
| msgpack | Protobuf 대비 스키마 진화·코드 생성 미흡 |
| FlatBuffers | 게임/HPC 특화. 일반 RPC에 과한 복잡도 |

## 참고 자료
- [Protocol Buffers Language Guide](https://protobuf.dev/programming-guides/proto3/)
- [google/protobuf](https://github.com/protocolbuffers/protobuf)
- [ADR-001](./001-scope-and-philosophy.md) — 검증된 기술 우선 원칙
- [ADR-003](./003-server-mesh-and-discovery.md) — SWIM+Gossip 서비스 검색
- [ADR-005](./005-mesh-transport-quic.md) — QUIC relay 전송
