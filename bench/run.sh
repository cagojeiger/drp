#!/bin/bash
set -e

cd "$(dirname "$0")"

REQUESTS=${1:-10000}
CONCURRENCY=${2:-50}
REPEATS=${3:-3}
DURATION_SEC=${4:-${DURATION_SEC:-0}}
PORT=18080
TMP_DIR=$(mktemp -d)
KEEP_TMP=${KEEP_TMP:-1}
if [ "$KEEP_TMP" = "1" ]; then
    trap 'echo "Artifacts kept: $TMP_DIR"' EXIT
else
    trap 'rm -rf "$TMP_DIR"' EXIT
fi

echo "=== drps vs frps Benchmark ==="
if [ "$DURATION_SEC" -gt 0 ]; then
    echo "Mode: duration (${DURATION_SEC}s), Concurrency: $CONCURRENCY, Repeats: $REPEATS"
else
    echo "Mode: fixed requests ($REQUESTS), Concurrency: $CONCURRENCY, Repeats: $REPEATS"
    EFFECTIVE_REQUESTS=$(( (REQUESTS / CONCURRENCY) * CONCURRENCY ))
    if [ "$EFFECTIVE_REQUESTS" -ne "$REQUESTS" ]; then
        echo "Note: hey truncates requests to a multiple of concurrency ($REQUESTS -> $EFFECTIVE_REQUESTS)."
    fi
fi
echo ""

# Check hey
HEY=$(command -v hey || echo "$HOME/go/bin/hey")
if [ ! -x "$HEY" ]; then
    echo "Error: hey not found. Install with: go install github.com/rakyll/hey@latest"
    exit 1
fi

wait_for_proxy() {
    local compose_file=$1
    local max_wait=30
    local elapsed=0
    echo "--- Waiting for proxy registration ---"
    while [ $elapsed -lt $max_wait ]; do
        if docker compose -f "$compose_file" logs frpc 2>&1 | grep -q "start proxy success"; then
            echo "    Proxy registered (${elapsed}s)"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    echo "    WARNING: proxy not registered after ${max_wait}s"
    return 1
}

ws_bench() {
    local port=$1
    local count=${2:-500}
    local concurrency=${3:-10}
    go run ws_test_runner.go "$port" "$count" "$concurrency"
}

maybe_capture_pprof() {
    local name=$1
    local iter=$2
    if [ "$name" != "drps" ]; then
        return
    fi
    if [ "${ENABLE_PPROF:-0}" != "1" ]; then
        return
    fi
    local sec=${PPROF_SECONDS:-5}
    local out="$TMP_DIR/${name}_${iter}_cpu.pprof"
    (
      curl -s "http://localhost:$PORT/debug/pprof/profile?seconds=${sec}" -o "$out" || true
    ) &
    PPROF_PID=$!
}

parse_http() {
    local file=$1
    local rps avg p50 p95 p99 status_total status_non2xx err_calc err_dist err_total
    rps=$(awk '/Requests\/sec:/{print $2; exit}' "$file")
    avg=$(awk '/Average:/{print $2; exit}' "$file")
    p50=$(awk '/50%% in/{print $3; exit}' "$file")
    p95=$(awk '/95%% in/{print $3; exit}' "$file")
    p99=$(awk '/99%% in/{print $3; exit}' "$file")
    status_total=$(awk '/\[[0-9]+\][[:space:]]+[0-9]+ responses/{sum+=$2} END{print sum+0}' "$file")
    status_non2xx=$(awk '
      /\[[0-9]+\][[:space:]]+[0-9]+ responses/ {
        code=$1; gsub(/\[|\]/,"",code); cnt=$2+0;
        if (code < 200 || code >= 300) sum += cnt
      }
      END{print sum+0}
    ' "$file")
    if [ "$DURATION_SEC" -gt 0 ]; then
        err_calc=0
    else
        err_calc=$((EFFECTIVE_REQUESTS-status_total))
    fi
    err_dist=$(awk '
      /Error distribution:/{flag=1; next}
      flag && /^\s*\[[0-9]+\]/ {gsub(/\[|\]/,"",$1); sum+=$1}
      flag && NF==0 {flag=0}
      END{print sum+0}
    ' "$file")
    if [ "$DURATION_SEC" -gt 0 ]; then
        err_total=$((status_non2xx + err_dist))
    else
        err_total=$((status_non2xx + err_dist + err_calc))
    fi
    echo "$rps,$avg,$p50,$p95,$p99,$status_total,$err_total"
}

parse_ws() {
    local file=$1
    local rps avg
    rps=$(awk '/RPS:/{print $2; exit}' "$file")
    avg=$(awk '/Avg latency:/{print $3; exit}' "$file")
    echo "$rps,$avg"
}

run_once() {
    local name=$1
    local compose_file=$2
    local iter=$3

    echo "=========================================="
    echo "  $name (run $iter/$REPEATS)"
    echo "=========================================="

    echo "--- Starting $name ---"
    docker compose -f "$compose_file" up -d --build 2>&1 | tail -3

    wait_for_proxy "$compose_file"

    # Health check
    local status=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: bench.local" "http://localhost:$PORT/")
    echo "    HTTP health check: $status"

    # HTTP Benchmark
    echo "--- Warmup ---"
    "$HEY" -n 500 -c 10 -host "bench.local" "http://localhost:$PORT/" > /dev/null 2>&1
    sleep 1

    echo "--- HTTP Benchmark ---"
    local http_out="$TMP_DIR/${name}_${iter}_http.txt"
    maybe_capture_pprof "$name" "$iter"
    if [ "$DURATION_SEC" -gt 0 ]; then
        "$HEY" -z "${DURATION_SEC}s" -c "$CONCURRENCY" -host "bench.local" "http://localhost:$PORT/" | tee "$http_out"
    else
        "$HEY" -n "$REQUESTS" -c "$CONCURRENCY" -host "bench.local" "http://localhost:$PORT/" | tee "$http_out"
    fi
    if [ -n "${PPROF_PID:-}" ]; then
        wait "$PPROF_PID" || true
        unset PPROF_PID
    fi

    # WebSocket Benchmark
    echo ""
    echo "--- WebSocket Benchmark (500 connections, concurrency 10) ---"
    local ws_out="$TMP_DIR/${name}_${iter}_ws.txt"
    ws_bench "$PORT" 500 10 | tee "$ws_out"

    local http_metrics ws_metrics
    http_metrics=$(parse_http "$http_out")
    ws_metrics=$(parse_ws "$ws_out")
    echo "$iter,$http_metrics,$ws_metrics" >> "$TMP_DIR/${name}.csv"

    IFS=, read -r _http_rps _http_avg _http_p50 _http_p95 _http_p99 _http_status_total _http_err <<< "$http_metrics"
    if [ "${_http_err:-0}" -gt 0 ]; then
        echo ""
        echo "--- HTTP error details (${name} run ${iter}) ---"
        awk '
          /Error distribution:/{flag=1; print; next}
          flag {print}
          flag && NF==0 {flag=0}
        ' "$http_out"
        echo "--- Container log hints (${name} run ${iter}) ---"
        if command -v rg >/dev/null 2>&1; then
            docker compose -f "$compose_file" logs --since 2m 2>&1 | rg -i "error|timeout|reset|broken pipe|refused|closed|eof" || true
        else
            docker compose -f "$compose_file" logs --since 2m 2>&1 | grep -Ei "error|timeout|reset|broken pipe|refused|closed|eof" || true
        fi
    fi

    if [ "$name" = "drps" ]; then
        echo ""
        echo "--- drps internal metrics ---"
        curl -s "http://localhost:$PORT/__drps/metrics" | tee "$TMP_DIR/${name}_${iter}_metrics.json"
    fi

    echo ""
    echo "--- Stopping $name ---"
    docker compose -f "$compose_file" down 2>&1 | tail -3
    echo ""
    sleep 3
}

run_bench() {
    local name=$1
    local compose_file=$2
    : > "$TMP_DIR/${name}.csv"
    for ((i=1; i<=REPEATS; i++)); do
        run_once "$name" "$compose_file" "$i"
    done
}

summarize() {
    local name=$1
    local file="$TMP_DIR/${name}.csv"
    awk -F, '
      BEGIN{
        runs=0
        sum_http_rps=0; sumsq_http_rps=0
        sum_http_p50=0; sum_http_p95=0; sum_http_p99=0; sumsq_http_p99=0
        sum_http_err=0; sumsq_http_err=0
        sum_ws_rps=0; sumsq_ws_rps=0
      }
      NF>=10{
        runs++
        http_rps=$2+0
        p50=$4+0
        p95=$5+0
        p99=$6+0
        http_err=$8+0
        ws_rps=$9+0

        sum_http_rps += http_rps
        sumsq_http_rps += (http_rps*http_rps)

        sum_http_p50 += p50
        sum_http_p95 += p95
        sum_http_p99 += p99
        sumsq_http_p99 += (p99*p99)

        sum_http_err += http_err
        sumsq_http_err += (http_err*http_err)

        sum_ws_rps += ws_rps
        sumsq_ws_rps += (ws_rps*ws_rps)
      }
      END{
        if (runs==0) { print "0,0,0,0,0,0,0,0,0,0"; exit }
        mean_http_rps=sum_http_rps/runs
        mean_http_p50=sum_http_p50/runs
        mean_http_p95=sum_http_p95/runs
        mean_http_p99=sum_http_p99/runs
        mean_http_err=sum_http_err/runs
        mean_ws_rps=sum_ws_rps/runs

        var_http_rps=(sumsq_http_rps/runs)-(mean_http_rps*mean_http_rps)
        if (var_http_rps < 0) var_http_rps=0
        var_http_p99=(sumsq_http_p99/runs)-(mean_http_p99*mean_http_p99)
        if (var_http_p99 < 0) var_http_p99=0
        var_http_err=(sumsq_http_err/runs)-(mean_http_err*mean_http_err)
        if (var_http_err < 0) var_http_err=0
        var_ws_rps=(sumsq_ws_rps/runs)-(mean_ws_rps*mean_ws_rps)
        if (var_ws_rps < 0) var_ws_rps=0

        printf "%.2f,%.2f,%.4f,%.4f,%.4f,%.4f,%.2f,%.2f,%.2f,%.2f\n",
          mean_http_rps, sqrt(var_http_rps),
          mean_http_p50, mean_http_p95, mean_http_p99, sqrt(var_http_p99),
          mean_http_err, sqrt(var_http_err),
          mean_ws_rps, sqrt(var_ws_rps)
      }
    ' "$file"
}

run_bench "drps" "docker-compose.drps.yml"
run_bench "frps" "docker-compose.frps.yml"

drps_sum=$(summarize "drps")
frps_sum=$(summarize "frps")

IFS=, read -r drps_http_rps drps_http_rps_sd drps_http_p50 drps_http_p95 drps_http_p99 drps_http_p99_sd drps_http_err drps_http_err_sd drps_ws_rps drps_ws_rps_sd <<< "$drps_sum"
IFS=, read -r frps_http_rps frps_http_rps_sd frps_http_p50 frps_http_p95 frps_http_p99 frps_http_p99_sd frps_http_err frps_http_err_sd frps_ws_rps frps_ws_rps_sd <<< "$frps_sum"

echo ""
echo "=== Statistical Summary (${REPEATS} runs) ==="
printf "%-8s %-20s %-11s %-11s %-20s %-14s %-20s\n" "name" "http_rps(mean±sd)" "p50(s)" "p95(s)" "p99(s)(mean±sd)" "http_err(mean)" "ws_rps(mean±sd)"
printf "%-8s %-20s %-11s %-11s %-20s %-14s %-20s\n" "drps" "${drps_http_rps}±${drps_http_rps_sd}" "$drps_http_p50" "$drps_http_p95" "${drps_http_p99}±${drps_http_p99_sd}" "$drps_http_err" "${drps_ws_rps}±${drps_ws_rps_sd}"
printf "%-8s %-20s %-11s %-11s %-20s %-14s %-20s\n" "frps" "${frps_http_rps}±${frps_http_rps_sd}" "$frps_http_p50" "$frps_http_p95" "${frps_http_p99}±${frps_http_p99_sd}" "$frps_http_err" "${frps_ws_rps}±${frps_ws_rps_sd}"

echo "=== Done ==="
