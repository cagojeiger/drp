# E2E 테스트

`test/` 대상. Docker (testcontainers-go) + 실제 frpc v0.68.0. 주 테스트 11개(+별칭 테스트).

## 환경

```
testcontainers-go로 Docker 컨테이너 자동 관리:
  - drps: Dockerfile로 빌드
  - frpc: GitHub releases에서 v0.68.0 다운로드
  - backend: nginx:alpine
  - ws-echo: test/ws-echo 빌드 (WebSocket echo 서버)
  - 네트워크: 컨테이너간 Docker 네트워크
```

## 기본 기능 (4)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-01 | TestFrpcLoginSuccess | P0 | 실제 frpc → drps Login → "login to server success" 로그 |
| E-02 | TestFrpcFullPipeline | P0 | frpc → nginx 백엔드 → HTTP 200 + body에 "nginx" 포함 |
| E-03 | TestFrpcNotFoundDomain | P1 | 미등록 도메인 → 404 응답 |
| E-04 | TestFrpcMultipleProxies | P0 | 2개 프록시 (site-a, site-b) → 각각 독립 200 응답 |

## HTTP 부하 (3)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-05 | TestHTTPConcurrentProxyNo5xx | P0 | 120 req / 20 concurrency → 전부 200 + non-2xx = 0 |
| E-06 | TestHTTPBurst1000NoNon2xx | P0 | 1000 req / 50 concurrency → non-2xx = 0, failed = 0 |
| E-07 | TestWebSocketE2E | P0 | drps 경유 WS upgrade (Host: ws.local) → 101 + masked frame echo 성공 |

별칭: `TestHTTPConcurrentProxy` → E-05, `TestHTTPLoadZeroErrors` → E-06.

## WebSocket 부하 (2)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-08 | TestWSBurst200NoFail | P0 | 200 conn / 10 concurrency → fail = 0 |
| E-09 | TestWSBurst500NoFail | P1 | 500 conn / 10 concurrency → fail = 0 |

별칭: `TestWebSocketConcurrent` → E-08.

## 메트릭 (2)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-10 | TestMetricsEndpointAfterTraffic | P1 | 20 req 후 GET /__drps/metrics → 200 + JSON + requested > 0, sent > 0, get_hit > 0 |
| E-11 | TestMetricsInflightZeroAfterBurst | P1 | 300 req / 30 concurrency 후 메트릭 → inflight = 0 |

별칭: `TestMetricsEndpoint` → E-10, `TestMetricsAfterLoad` → E-11, `TestIB` → E-06.

## cmd/drps 단위 테스트 (별도 패키지 1)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| E-12 | TestServerReadHeaderTimeout | P2 | http.Server ReadHeaderTimeout = 60s 설정 확인 |

위 테스트는 `cmd/drps/main_test.go`에 있으며 `./test` 패키지가 아니라 별도 실행 대상이다.

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
| HTTP 동시접속 부하 | X | O (최대 1000 req / 50 concurrency) |
| WebSocket 동시접속 부하 | X | O (최대 500 conn / 10 concurrency) |
| 메트릭 엔드포인트 | X | O (/__drps/metrics JSON 검증) |
| 메트릭 inflight 정합성 | X | O (burst 후 inflight = 0) |

## 실행

```bash
# E2E (Docker 필요)
go test ./test/ -v -timeout 300s

# 단위 테스트만 (Docker 불필요)
go test github.com/kangheeyong/drp/internal/... -v

# 짧은 모드 (e2e 건너뛰기)
go test ./test/ -short

# cmd/drps 검증
go test ./cmd/drps -v

# 벤치마크 (Docker 불필요)
go test github.com/kangheeyong/drp/internal/... -bench=. -benchmem
```
