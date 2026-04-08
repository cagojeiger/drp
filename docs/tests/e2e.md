# E2E 테스트

`test/` 대상. Docker (testcontainers-go) + 실제 frpc v0.68.0. 7개 (bench 기반 3개 추가).

## 환경

```
testcontainers-go로 Docker 컨테이너 자동 관리:
  - drps: Dockerfile로 빌드
  - frpc: GitHub releases에서 v0.68.0 다운로드
  - backend: nginx:alpine
  - 네트워크: 컨테이너간 Docker 네트워크
```

## 테스트

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-01 | TestFrpcLoginSuccess | P0 | 실제 frpc → drps Login → "login to server success" 로그 |
| E-02 | TestFrpcFullPipeline | P0 | frpc → nginx 백엔드 → HTTP 200 + body에 "nginx" 포함 |
| E-03 | TestFrpcNotFoundDomain | P1 | 미등록 도메인 → 404 응답 |
| E-04 | TestFrpcMultipleProxies | P0 | 2개 프록시 (site-a, site-b) → 각각 독립 200 응답 |

## 부하/WebSocket/메트릭 (bench 기반 추가)

ws-echo 컨테이너 추가 필요: bench/ws-echo Dockerfile 빌드.

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-05 | TestHTTPConcurrentProxy | P0 | 50 goroutine 동시 GET → 전부 200 + body "nginx" + non-2xx = 0 |
| E-06 | TestWebSocketE2E | P0 | drps 경유 WS upgrade (Host: ws.local) → 101 + masked frame echo 성공 |
| E-07 | TestMetricsEndpoint | P1 | GET /__drps/metrics → 200 + JSON 파싱 + 요청 후 requested > 0 |

### E-05 검증 방법

```go
func TestHTTPConcurrentProxy(t *testing.T) {
    // drps + frpc + backend 컨테이너 기동
    // 50 goroutine에서 동시 HTTP GET (Host: test.local)
    // 전부 200 + body contains "nginx"
    // error count == 0
}
```

### E-06 검증 방법

```go
func TestWebSocketE2E(t *testing.T) {
    // drps + frpc + ws-echo 컨테이너 기동
    // TCP 연결 → HTTP Upgrade (Host: ws.local)
    // 101 Switching Protocols 확인
    // masked text frame "hello" 전송
    // echo 프레임 수신 → payload == "hello"
}
```

### E-07 검증 방법

```go
func TestMetricsEndpoint(t *testing.T) {
    // drps + frpc + backend 컨테이너 기동
    // HTTP 요청 10개 전송
    // GET /__drps/metrics → 200 + Content-Type: application/json
    // JSON 파싱 → requested > 0, sent > 0
}
```

## 검증 범위

| 항목 | 단위 테스트 | E2E |
|------|-----------|-----|
| 와이어 프로토콜 호환 | O (포맷 검증) | O (실제 frpc 통신) |
| 인증 | O | O |
| AES 암호화 | O | O (frpc 기본 제어채널) |
| yamux 멀티플렉싱 | O (net.Pipe) | O (실제 TCP) |
| HTTP 프록시 | O (fakeFrpc) | O (실제 frpc + nginx) |
| 멀티 프록시 | O | O |
| WebSocket | O (단위) | O (실제 drps + ws-echo) |
| HTTP 동시접속 | X | O (50 goroutine) |
| 메트릭 엔드포인트 | X | O (/__drps/metrics) |

## 실행

```bash
# 전체 (Docker 필요)
go test ./test/ -v -timeout 300s

# 단위 테스트만 (Docker 불필요)
go test github.com/kangheeyong/drp/internal/... -v

# 짧은 모드 (e2e 건너뛰기)
go test ./test/ -short

# 벤치마크 (Docker 불필요)
go test github.com/kangheeyong/drp/internal/... -bench=. -benchmem
```
