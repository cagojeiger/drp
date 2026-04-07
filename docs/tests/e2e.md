# E2E 테스트

`test/` 대상. Docker (testcontainers-go) + 실제 frpc v0.68.0. 4개.

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

## 검증 범위

| 항목 | 단위 테스트 | E2E |
|------|-----------|-----|
| 와이어 프로토콜 호환 | ✅ (포맷 검증) | ✅ (실제 frpc 통신) |
| 인증 | ✅ | ✅ |
| AES 암호화 | ✅ | ✅ (frpc 기본 제어채널) |
| yamux 멀티플렉싱 | ✅ (net.Pipe) | ✅ (실제 TCP) |
| HTTP 프록시 | ✅ (fakeFrpc) | ✅ (실제 frpc + nginx) |
| 멀티 프록시 | ✅ | ✅ |
| WebSocket | ✅ (단위) | 로컬 검증 (Go raw client) |

## 실행

```bash
# 전체 (Docker 필요)
go test ./test/ -v -timeout 300s

# 단위 테스트만 (Docker 불필요)
go test github.com/kangheeyong/drp/internal/... -v

# 짧은 모드 (e2e 건너뛰기)
go test ./test/ -short
```
