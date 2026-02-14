# AGENTS.md — drp 프로젝트 컨벤션

## 브랜치 전략

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

### 규칙

- `main`에 직접 커밋/푸시 금지. 반드시 `dev` → `main` PR 경유
- 기능 개발은 `dev`에서 `feature/*` 브랜치를 분기하여 작업 후 `dev`로 머지
- 문서 작업은 `docs/*` 브랜치 또는 `dev`에서 직접 가능

## ADR (Architecture Decision Record) 컨벤션

### 위치

`docs/adr/` 디렉토리에 저장.

### 파일명

`{3자리 번호}-{kebab-case-제목}.md`

예: `000-repository-strategy.md`, `001-minimalist-architecture.md`

### 구조

```markdown
# ADR-{번호}: {제목}

## 상태
Proposed | Accepted | Superseded by [ADR-XXX](./XXX-xxx.md)

## 컨텍스트
왜 이 결정이 필요한지. 배경과 제약사항.

## 결정
무엇을 선택했는지. 상세 내용 (표, 다이어그램, 코드 포함 가능).

## 결과

### 장점
- ...

### 단점
- ...

### 대안 (선택하지 않음)
| 대안 | 미선택 이유 |
|------|------------|
| ... | ... |

## 참고 자료
- 링크, 관련 ADR 등
```

### 상태 값

| 상태 | 의미 |
|------|------|
| `Proposed` | 제안됨. 검토 대기 |
| `Accepted` | 수락됨. 현재 유효 |
| `Superseded by [ADR-XXX]` | 다른 ADR로 대체됨 |

### 번호 규칙

- `000` — 리포지토리/브랜치 전략
- `001~` — 설계 결정 (시간순)
- 번호는 절대 재사용하지 않음

## 코드 컨벤션

- 언어: Go
- 모듈: `github.com/cagojeiger/drp`
- 패키지: 단일 패키지 (`package main`)
- 목표: ~750줄, 10파일
- 스코프: **HTTP/HTTPS 전용** (순수 TCP, UDP 미지원)

## 핵심 설계 결정

| 결정 | 선택 | 이유 |
|------|------|------|
| 프로토콜 스코프 | HTTP/HTTPS만 | 하나만 잘 한다. TCP/UDP까지 가면 frp 복잡도 재현 |
| 라우팅 | Host 헤더 / TLS SNI | 포트 2개로 무제한 서비스. 포트 고갈 없음 |
| 암호화 | TLS (Go 표준 라이브러리) | LB 뒤 배치 가능. K8s 네이티브. WireGuard 대비 배포 단순 |
| 분산 상태 | Redis | 간단, 충분한 성능, TTL 기반 자동 정리 |
| 멀티플렉싱 | yamux | 검증된 라이브러리, 단일 TCP 위 다중 스트림 |
| 메시지 포맷 | TLV + JSON | 제어 메시지는 빈도 낮음, 디버깅 용이 |
| HA | 멀티 연결 (기본 2) | Cloudflare Tunnel 패턴. LB 통해 다른 drps에 연결 |

## 참고 소스

- frp 원본: `.repos/frp/` (git submodule)
- 설계 문서: `CONTEXT.md`
- Cloudflare Tunnel: `cloudflare/cloudflared` (HA 패턴 참고)
