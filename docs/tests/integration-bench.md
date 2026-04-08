# 통합 벤치마크 테스트 시나리오

bench/ 디렉토리의 Docker 기반 테스트에서 추출. testcontainers-go로 전환 대상.

## 환경 구성

```
testcontainers-go로 Docker 컨테이너 자동 관리:
  - drps: Dockerfile로 빌드
  - frpc: .repos/frp 소스 빌드 또는 GitHub release v0.68.0
  - backend: nginx:alpine (HTTP 프록시 대상)
  - ws-echo: bench/ws-echo 빌드 (WebSocket echo 서버)
  - 네트워크: 컨테이너간 Docker 네트워크
```

## A. HTTP 부하 테스트

기존 bench/run.sh + hey 기반. 통합 테스트에서는 기능 정합성 + 기본 부하 검증.

| ID | 시나리오 | 중요도 | 검증 | bench 원본 |
|----|---------|--------|------|-----------|
| IB-01 | HTTP 동시접속 프록싱 | P0 | N개 동시 요청 → 전부 200 + body 정합 | run.sh hey -c 50 |
| IB-02 | HTTP 부하 시 에러율 0% | P0 | 1000 req / 50 concurrency → non-2xx = 0 | run.sh 에러 파싱 |
| IB-03 | Pool exhaustion 복구 | P1 | pool 소진 후 refill 대기 → 요청 성공 | run.sh 대량 요청 시 pool 동작 |
| IB-04 | Warmup 후 latency 안정 | P2 | warmup 500 req 후 본 측정 p99 편차 < 2x p50 | run.sh warmup 구간 |

### IB-01 검증 방법

```go
func TestHTTPConcurrentProxy(t *testing.T) {
    // drps + frpc + backend 컨테이너 기동
    // 50 goroutine에서 동시 HTTP GET
    // 전부 200 + body contains "nginx"
}
```

### IB-02 검증 방법

```go
func TestHTTPLoadZeroErrors(t *testing.T) {
    // drps + frpc + backend 컨테이너 기동
    // 1000 req, 50 concurrency
    // non-2xx count == 0
    // connection error count == 0
}
```

## B. WebSocket 통합 테스트

기존 bench/ws_test_runner.go 기반. raw TCP WebSocket 핸드셰이크 + 프레임 echo.

| ID | 시나리오 | 중요도 | 검증 | bench 원본 |
|----|---------|--------|------|-----------|
| IB-05 | WebSocket upgrade + echo (Docker) | P0 | drps 경유 WS upgrade → 프레임 echo 성공 | ws_test_runner.go doWS() |
| IB-06 | WebSocket 동시접속 | P1 | 50 동시 WS 연결 → 전부 echo 성공 | ws_test_runner.go concurrency |
| IB-07 | WebSocket 에러율 0% | P1 | 200 conn / 10 concurrency → fail = 0 | ws_test_runner.go 성공률 |

### IB-05 검증 방법

```go
func TestWebSocketE2E(t *testing.T) {
    // drps + frpc + ws-echo 컨테이너 기동
    // TCP 연결 → HTTP Upgrade (Host: ws.local)
    // 101 Switching Protocols 확인
    // masked text frame "hello" 전송
    // echo 프레임 수신 → payload == "hello"
}
```

### IB-06 검증 방법

```go
func TestWebSocketConcurrent(t *testing.T) {
    // drps + frpc + ws-echo 컨테이너 기동
    // 50 goroutine에서 동시 WS 연결+echo
    // 전부 success
}
```

## C. 메트릭 엔드포인트

기존 bench/run.sh의 `/__drps/metrics` 조회.

| ID | 시나리오 | 중요도 | 검증 | bench 원본 |
|----|---------|--------|------|-----------|
| IB-08 | 메트릭 엔드포인트 응답 | P1 | GET /__drps/metrics → 200 + JSON | run.sh curl metrics |
| IB-09 | 부하 후 메트릭 정합성 | P1 | 요청 N개 후 메트릭 값 > 0 (requested, sent 등) | run.sh 메트릭 수집 |

### IB-08 검증 방법

```go
func TestMetricsEndpoint(t *testing.T) {
    // drps + frpc 컨테이너 기동
    // GET /__drps/metrics
    // 200 + Content-Type: application/json
    // JSON 파싱 성공
}
```

### IB-09 검증 방법

```go
func TestMetricsAfterLoad(t *testing.T) {
    // drps + frpc + backend 컨테이너 기동
    // HTTP 요청 100개 전송
    // GET /__drps/metrics
    // requested > 0, sent > 0
}
```

## D. pprof 프로파일링

기존 bench/run.sh의 ENABLE_PPROF 옵션.

| ID | 시나리오 | 중요도 | 검증 | bench 원본 |
|----|---------|--------|------|-----------|
| IB-10 | pprof 엔드포인트 활성화 | P2 | DRPS_PPROF=1 → /debug/pprof/ 200 응답 | run.sh maybe_capture_pprof |

## E. 연결 안정성

bench 반복 실행 중 발견되던 문제 시나리오.

| ID | 시나리오 | 중요도 | 검증 | bench 원본 |
|----|---------|--------|------|-----------|
| IB-11 | 장시간 연결 유지 | P1 | frpc keepalive → 30초 후에도 프록시 정상 | docker-compose yamux keepalive |
| IB-12 | 대량 요청 후 pool 상태 | P1 | 10000 req 후 pool이 refill 가능 상태 | run.sh REQUESTS=10000 |

## 기존 테스트와의 관계

| bench 시나리오 | 기존 커버리지 | 통합 테스트 추가 필요 |
|---------------|-------------|-------------------|
| HTTP GET 프록싱 | e2e.md E-02 (단건) | IB-01, IB-02 (동시접속+부하) |
| 미등록 도메인 404 | e2e.md E-03 | 불필요 |
| 복수 프록시 | e2e.md E-04 | 불필요 |
| WebSocket echo | proxy.md X-16 (unit, net.Pipe) | IB-05 (Docker, 실제 ws-echo) |
| WebSocket 동시접속 | 없음 | IB-06, IB-07 |
| 메트릭 엔드포인트 | 없음 | IB-08, IB-09 |
| pprof | 없음 | IB-10 |
| Pool exhaustion | pool.md P-02, P-03 (unit) | IB-03 (Docker, 실제 부하) |

## 우선순위

1. **P0**: IB-01, IB-02, IB-05 — 동시접속 HTTP/WS 기본 검증
2. **P1**: IB-03, IB-06, IB-07, IB-08, IB-09, IB-11, IB-12 — 부하+안정성
3. **P2**: IB-04, IB-10 — latency 안정성, pprof

## 실행

```bash
# 전체 통합 벤치마크 테스트 (Docker 필요)
go test ./test/ -v -run TestIB -timeout 600s

# 짧은 모드 (건너뛰기)
go test ./test/ -short
```
