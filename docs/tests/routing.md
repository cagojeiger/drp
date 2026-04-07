# 라우팅 테스트

`internal/router` 대상. 10개.

## 기본 CRUD (4)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| R-01 | TestAddAndLookup | P0 | 등록 → Lookup → RouteConfig 반환 |
| R-02 | TestLookupNotFound | P0 | 미등록 도메인 → false |
| R-03 | TestDuplicateDomain | P0 | 같은 도메인+경로 중복 → error |
| R-04 | TestRemove | P0 | Remove → Lookup → false |

## 제거 (3)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| R-05 | TestRemoveNonExistent | P2 | 없는 proxyName → 에러 없이 무시 |
| R-06 | TestRemoveMultipleRoutes | P0 | 같은 proxyName의 여러 도메인 일괄 제거 |
| R-10 | TestRemoveAfterAddSameDomain | P1 | 제거 후 같은 도메인 재등록 가능 |

## 매칭 (3)

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| R-07 | TestLongestPrefixMatch | P0 | /api/v2 > /api > / |
| R-08 | TestWildcardDomain | P1 | *.example.com 매칭 규칙 |
| R-09 | TestExactDomainPriority | P1 | 정확한 도메인이 와일드카드보다 우선 |
