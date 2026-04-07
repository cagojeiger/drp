# 성능 테스트

벤치마크 12개. 리팩토링 전후 비교 및 회귀 방지 목적.

## 실행

```bash
# 전체 벤치마크
go test ./internal/... -bench=. -benchmem -count=3

# 패키지별
go test ./internal/msg/ -bench=. -benchmem
go test ./internal/crypto/ -bench=. -benchmem
go test ./internal/proxy/ -bench=. -benchmem
go test ./internal/pool/ -bench=. -benchmem
```

## msg 벤치마크 (4) — internal/msg

| ID | 벤치마크 | 개선 포인트 | 기대 효과 |
|----|---------|------------|----------|
| B-M-01 | BenchmarkWriteMsg | 단일 버퍼 Write (3 syscall → 1) | allocs/op 감소, ns/op 감소 |
| B-M-02 | BenchmarkReadMsg | body 버퍼 sync.Pool 재사용 | allocs/op 감소 |
| B-M-03 | BenchmarkTypeOf | switch assertion (fmt.Sprintf 제거) | 0 allocs/op |
| B-M-04 | BenchmarkMsgRoundTrip | Write+Read 전체 경로 | throughput 기준선 |

### B-M-01 검증 방법

```go
func BenchmarkWriteMsg(b *testing.B) {
    w := io.Discard
    m := &msg.Ping{}
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        msg.WriteMsg(w, m)
    }
}
```

**기대**: 1 allocs/op (버퍼 1개), 기존 대비 syscall 횟수 1/3.

### B-M-03 검증 방법

```go
func BenchmarkTypeOf(b *testing.B) {
    m := &msg.Login{}
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        msg.TypeOf(m)
    }
}
```

**기대**: 0 allocs/op. 기존 fmt.Sprintf 방식은 1+ allocs/op.

## crypto 벤치마크 (3) — internal/crypto

| ID | 벤치마크 | 개선 포인트 | 기대 효과 |
|----|---------|------------|----------|
| B-C-01 | BenchmarkDeriveKey | PBKDF2 비용 측정 | 캐싱 필요성 정량적 근거 |
| B-C-02 | BenchmarkCryptoRoundTrip | AES 암복호화 throughput | 기준선 |
| B-C-03 | BenchmarkSnappyRoundTrip | snappy 압축/해제 throughput | 기준선 |

### B-C-01 검증 방법

```go
func BenchmarkDeriveKey(b *testing.B) {
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        crypto.DeriveKey("test-token")
    }
}
```

**기대**: ~50us/op 이상. 이 수치가 매 HTTP 요청마다 반복되면 병목.
캐싱 후에는 이 비용이 서버 시작 시 1회로 제한.

## proxy 벤치마크 (3) — internal/proxy

| ID | 벤치마크 | 개선 포인트 | 기대 효과 |
|----|---------|------------|----------|
| B-X-01 | BenchmarkServeHTTP | 전체 hot path | 종합 throughput |
| B-X-03 | BenchmarkWrap | 캐시 키 사용 | DeriveKey 제거 효과 |
| B-X-04 | BenchmarkConnTransportRoundTrip | bufio sync.Pool 재사용 | allocs/op 감소 |

## pool 벤치마크 (2) — internal/pool

| ID | 벤치마크 | 개선 포인트 | 기대 효과 |
|----|---------|------------|----------|
| B-X-02 | BenchmarkPoolGetPut | Get/Put 사이클 | 기준선 |
| B-X-05 | BenchmarkPoolLookupByRunID | RunID 직접 vs RangeByProxy | O(1) vs O(N) 차이 |

### B-X-05 검증 방법

```go
func BenchmarkPoolLookupByRunID(b *testing.B) {
    reg := pool.NewRegistry()
    for i := 0; i < 100; i++ {
        reg.GetOrCreate(fmt.Sprintf("run-%d", i), func() {})
    }
    b.ResetTimer()
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        reg.Get("run-50")
    }
}
```

**기대**: O(1) — 프록시 수와 무관한 일정 시간.

## 목표 수치

| 지표 | 리팩토링 전 | 리팩토링 후 목표 |
|------|-----------|----------------|
| WriteMsg allocs/op | 3+ | 1 |
| TypeOf allocs/op | 1+ | 0 |
| DeriveKey 호출/req | 1 | 0 (캐시) |
| Pool lookup 복잡도 | O(N) | O(1) |
| WriteMsg syscall/call | 3 | 1 |
| bufio.Reader alloc/req | 1 | 0 (sync.Pool) |

## 회귀 방지

CI에서 벤치마크 실행 후 `benchstat`으로 비교:

```bash
# 기준선 저장
go test ./internal/... -bench=. -benchmem -count=5 > bench_before.txt

# 리팩토링 후
go test ./internal/... -bench=. -benchmem -count=5 > bench_after.txt

# 비교
benchstat bench_before.txt bench_after.txt
```
