# References — Distributed Systems Failure Testing

분산 시스템 장애 테스팅 관련 문헌 자료 정리.

drp의 테스트 전략 수립에 사용된 이론적 근거와 실전 패턴을 카테고리별로 분류한다.

## 카테고리

| 문서 | 내용 |
|------|------|
| [failure-models.md](./failure-models.md) | 장애 모델 분류 체계 (Kleppmann, Tanenbaum) |
| [testing-methodology.md](./testing-methodology.md) | 테스팅 방법론 (OSDI 2014, Jepsen, Netflix Chaos, Google SRE) |
| [go-testing-patterns.md](./go-testing-patterns.md) | Go 장애 시뮬레이션 패턴 (net.Pipe, faultConn, HashiCorp) |
| [drp-failure-matrix.md](./drp-failure-matrix.md) | drp 장애 테스트 매트릭스 (무엇을 어떻게 테스트할 것인가) |

## 읽기 순서

```
failure-models.md        → 장애가 뭔지 이해
  ↓
testing-methodology.md   → 뭘 테스트해야 하는지 확신
  ↓
go-testing-patterns.md   → 어떻게 구현하는지 패턴 학습
  ↓
drp-failure-matrix.md    → drp에 적용할 구체적 테스트 목록
```
