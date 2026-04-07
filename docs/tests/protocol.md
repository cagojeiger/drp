# 프로토콜 테스트

`internal/msg`, `internal/auth`, `internal/crypto` 대상. 24개.

## 와이어 포맷 (6) — internal/msg

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| M-01 | TestTypeBytes | P0 | 10개 메시지 타입 바이트 매핑 정확성 |
| M-02 | TestWriteMsg | P0 | 인코딩: 1B type + 8B length + JSON body |
| M-03 | TestReadMsg | P0 | 디코딩: 바이트 스트림 → 구조체 |
| M-04 | TestRoundTrip | P0 | 10개 메시지 Write → Read 왕복 무결성 |
| M-05 | TestReadMsgUnknownType | P1 | 알 수 없는 타입 바이트 에러 처리 |
| M-06 | TestReadMsgMaxSize | P1 | 10240 bytes 초과 메시지 거부 |

## 인증 (6) — internal/auth

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| A-01 | TestBuildAuthKey | P0 | MD5(token + timestamp) 계산 정확성 |
| A-02 | TestBuildAuthKeyEmptyToken | P2 | 빈 토큰 처리 |
| A-03 | TestVerifyAuth | P0 | 올바른 키 → true |
| A-04 | TestVerifyAuthWrongToken | P0 | 잘못된 토큰 → false |
| A-05 | TestVerifyAuthWrongTimestamp | P1 | 잘못된 타임스탬프 → false |
| A-06 | TestVerifyAuthTimingAttack | P1 | constant-time 비교 동작 확인 |

## 암호화 (7) — internal/crypto

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| C-01 | TestDeriveKey | P0 | PBKDF2 키 파생: 16바이트, 결정적, 토큰별 고유 |
| C-02 | TestDeriveKeyEmptyToken | P2 | 빈 토큰 키 파생 |
| C-03 | TestNewCryptoReadWriter | P0 | 암호문 ≠ 평문 |
| C-04 | TestCryptoRoundTrip | P0 | AES 암호화 → 복호화 왕복 |
| C-05 | TestCryptoRoundTripMultipleWrites | P1 | 여러 번 Write → 한 번 Read |
| C-06 | TestCryptoWrongKey | P0 | 잘못된 키 → 원본 복원 불가 |
| C-07 | TestCryptoLargeData | P1 | 1MB 데이터 왕복 무결성 |

## 압축 (5) — internal/crypto

| ID | 테스트 | 중요도 | 검증 |
|----|--------|--------|------|
| C-08 | TestSnappyRoundTrip | P0 | snappy 압축/해제 왕복 |
| C-09 | TestSnappyCompressed | P1 | 반복 데이터 압축 시 크기 감소 |
| C-10 | TestSnappyMultipleWrites | P1 | 여러 번 Write → Read |
| C-11 | TestSnappyLargeData | P1 | 1MB 데이터 왕복 |
| C-12 | TestSnappyEmpty | P2 | 빈 데이터 처리 |
