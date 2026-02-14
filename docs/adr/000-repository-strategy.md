# ADR-000: Repository & Branch Strategy

## 상태
Accepted

## 컨텍스트
drp 프로젝트의 브랜치 전략과 리포지토리 구조를 결정해야 합니다.
- 목표: 안정된 릴리즈와 개발 분리
- frp 원본 코드를 참고 소스로 유지해야 함
- 설계 결정을 ADR로 추적

## 결정

### 브랜치 전략

| 브랜치 | 용도 |
|--------|------|
| `main` | 안정된 릴리즈. 직접 푸시 금지 |
| `dev` | 개발 통합. 모든 작업은 여기서 시작 |
| `feature/*` | 기능 개발 (dev에서 분기) |
| `docs/*` | 문서 작업 (dev에서 분기) |

```
main (안정 릴리즈)
  ^
dev (개발 통합)
  ^
feature/* | docs/* (작업 브랜치)
```

### 머지 규칙

- `main`에 직접 커밋/푸시 금지. 반드시 `dev` → `main` PR 경유
- 기능 개발은 `dev`에서 `feature/*` 브랜치를 분기하여 작업 후 `dev`로 머지
- 문서 작업은 `docs/*` 브랜치 또는 `dev`에서 직접 가능

### 리포지토리 구조

```
drp/
├── AGENTS.md        # 프로젝트 컨벤션
├── CONTEXT.md       # 설계 문서 (전체 맥락)
├── docs/adr/        # Architecture Decision Records
├── .repos/frp/      # frp 원본 (git submodule)
└── *.go             # 소스 코드
```

### 참고 소스 관리

- frp 원본은 `.repos/frp/`에 git submodule로 관리
- 코드 분석 및 설계 참고용. 직접 의존하지 않음

## 결과

### 장점
- 안정된 릴리즈와 개발 분리
- frp 원본을 버전 고정하여 참고 가능
- ADR로 설계 결정 추적

### 단점
- 1인 개발에서는 다소 과한 구조일 수 있음

## 참고 자료
- [Martin Fowler - Branching Patterns](https://martinfowler.com/articles/branching-patterns.html)
