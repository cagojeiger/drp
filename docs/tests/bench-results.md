# Bench Results Log

최종 업데이트: 2026-04-08 (KST)

이 문서는 `bench/run.sh`로 실제 실행한 벤치/튜닝 실험 이력을 기록한다.

## 0) 측정 구조

```text
bench/run.sh
  ├─ drps 시나리오
  │   ├─ HTTP(hey)
  │   └─ WS(bench/ws_test_runner.go)
  └─ frps 시나리오
      ├─ HTTP(hey)
      └─ WS(bench/ws_test_runner.go)
```

## 1) 기본 비교 (6000 / 30 / 3)

- 명령:
  - `cd bench && PATH="$HOME/go/bin:$PATH" bash run.sh 6000 30 3`
- 결과(평균):

```text
name   http_rps   http_p99(s)  http_err  ws_rps
----   --------   -----------  --------  ------
drps   8453.31    0.0078       0.00      2218.10
frps   8279.36    0.0145       0.00      2509.83
```

- 해석:
  - HTTP는 drps가 비슷하거나 우위 구간이 있었음.
  - WS 연결 처리량은 frps 우위.

## 2) Wrap fast-path 적용 후 (enc/comp 미사용 시 raw conn)

- 관련 변경:
  - `/Users/kangheeyong/project/drp/internal/wrap/wrap.go`
- 재측정(6000 / 30 / 3):

```text
name   http_rps   http_p99(s)  http_err  ws_rps
----   --------   -----------  --------  ------
drps   8020.55    0.0087       0.00      2269.80
frps   8221.77    0.0115       0.00      2474.70
```

- 해석:
  - WS는 소폭 개선됐지만 전체 격차 해소는 못함.

## 3) yamux 파라미터 A/B (6000 / 30 / 3)

- 관련 변경:
  - `/Users/kangheeyong/project/drp/internal/config/config.go`
  - `/Users/kangheeyong/project/drp/cmd/drps/main.go`
  - `/Users/kangheeyong/project/drp/bench/docker-compose.drps.yml`

### 3-1) BASE
- 명령:
  - `bash run.sh 6000 30 3`
- 결과:

```text
drps   8030.05   0.0092   0.00   2308.43
frps   8790.15   0.0094   0.00   2268.90
```

### 3-2) TUNE-A (backlog=1024, keepalive=false)
- 명령:
  - `DRPS_YAMUX_ACCEPT_BACKLOG=1024 DRPS_YAMUX_KEEPALIVE_ENABLE=false bash run.sh 6000 30 3`
- 결과:

```text
drps   7921.15   0.0108   0.00   2171.77
frps   8535.06   0.0092   0.00   2274.27
```

### 3-3) TUNE-B (backlog=1024, keepalive=true)
- 명령:
  - `DRPS_YAMUX_ACCEPT_BACKLOG=1024 DRPS_YAMUX_KEEPALIVE_ENABLE=true bash run.sh 6000 30 3`
- 결과:

```text
drps   7514.48   0.0122   0.00   2043.00
frps   8707.99   0.0131   0.00   2538.93
```

- 해석:
  - 해당 yamux 튜닝 조합은 이득이 없었고 오히려 악화.

## 4) sendLoop 동적 배치/flush 실험 (6000 / 30 / 3)

- 관련 변경:
  - `/Users/kangheeyong/project/drp/internal/server/handle.go`
- 결과:

```text
name   http_rps   http_p99(s)  http_err  ws_rps
----   --------   -----------  --------  ------
drps   7902.34    0.0093       0.00      2374.17
frps   8691.22    0.0120       0.00      2579.57
```

- 해석:
  - WS는 일부 상승했지만 여전히 frps가 높음.

## 5) 장부하 통계 실험 (duration 60s, 10회)

- 명령:
  - `cd bench && PATH="$HOME/go/bin:$PATH" bash run.sh 10000 30 10 60`
- 요약(스크립트 출력):

```text
name     http_rps(mean±sd)   p50(s)   p95(s)   p99(mean±sd)    ws_rps(mean±sd)
drps     1665.61±83.01       0.0057   0.0735   0.1090±0.0151   2010.80±298.96
frps     1699.56±181.80      0.0054   0.0667   0.0986±0.0161   2391.47±137.59
```

- 상태코드 재집계(장부하):

```text
drps: total=999,812  200=758,843  non-200=240,969  non-200 rate=24.10%
frps: total=1,020,225 200=721,319 non-200=298,906 non-200 rate=29.30%
```

- Goodput(200 RPS) 재계산:

```text
drps goodput_200_rps = 1264.74 ± 52.65
frps goodput_200_rps = 1202.20 ± 142.93
```

- 해석:
  - 장부하에서는 양쪽 모두 non-200이 커서, `offered RPS`만으로 우열 판단이 왜곡됨.
  - 이 구간은 `RPS + p99 + non-2xx + goodput`를 함께 봐야 함.

## 6) 주의사항

- `duration` 모드에서는 기존 에러 집계 로직이 non-2xx를 누락할 수 있어 보정했다.
- 관련 파일:
  - `/Users/kangheeyong/project/drp/bench/run.sh`

## 7) 다음 권장 측정

```text
1) HTTP 전용 (60s x 10)
2) WS 연결 전용(conn/s)
3) WS 메시지 전용(reuse, msg/s)
```

