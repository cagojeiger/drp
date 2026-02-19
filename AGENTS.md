# AGENTS.md — drp 프로젝트 컨벤션

## 브랜치 전략

| 브랜치 | 용도 |
|--------|------|
| `main` | 안정된 릴리즈. 직접 푸시 금지 |
| `dev` | 개발 통합. 모든 작업은 여기서 시작 |
| `feature/*` | 기능 개발 (dev에서 분기) |
| `docs/*` | 문서 작업 (dev에서 분기) |

### 규칙

- `main`에 직접 커밋/푸시 금지. 반드시 `dev` → `main` PR 경유
- 기능 개발은 `dev`에서 `feature/*` 브랜치를 분기하여 작업 후 `dev`로 머지
- 문서 작업은 `docs/*` 브랜치 또는 `dev`에서 직접 가능

## ADR 컨벤션

### 위치 및 파일명

`docs/adr/{3자리 번호}-{kebab-case-제목}.md`

### 구조

```markdown
# ADR-{번호}: {제목}

## 상태
Proposed | Accepted | Superseded by [ADR-XXX](./XXX-xxx.md)

## 컨텍스트
## 결정
## 결과
## 참고 자료
```

### 현재 ADR 목록

| ADR | 제목 | 핵심 질문 |
|-----|------|----------|
| 001 | 스코프와 철학 | 뭘 하고 뭘 안 하나? |
| 002 | 호스트 기반 라우팅 | 요청을 어떤 서비스로 보내나? |
| 003 | 서버 메시와 서비스 검색 | 서버들이 어떻게 협력하나? |
| 004 | 프로토콜과 메시지 | 컴포넌트가 뭘로 대화하나? |
| 005 | Mesh 전송 계층 — QUIC | mesh를 뭘로 연결하나? |

## 핵심 설계 결정

| 결정 | 선택 | 이유 |
|------|------|------|
| 프로토콜 스코프 | HTTP/HTTPS만 | 하나만 잘 한다 |
| 라우팅 | Host 헤더 / TLS SNI | 포트 2개로 무제한 서비스 |
| 분산 상태 | Server Mesh (broadcast + relay) | 외부 의존성 제로 |
| 메시지 포맷 | TLV + JSON | 디버깅 용이. 기술 비종속 |
| Mesh 전송 | QUIC (drps↔drps), TCP (drpc↔drps) | 멀티플렉싱, HoL blocking 해소 |
| HA | 기본 1개 연결, 선택적 HA | mesh가 라우팅. HA는 fault tolerance 옵션 |

## 코드 컨벤션

- 모듈: `github.com/cagojeiger/drp`
- 스코프: **HTTP/HTTPS 전용**
- 원칙: 외부 의존성 제로, 인프라 중립, 기술 비종속

## 참고 소스

- frp 원본: `.repos/frp/` (git submodule)
- 설계 문서: `CONTEXT.md`
- ADR: `docs/adr/`
