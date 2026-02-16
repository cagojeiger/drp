#!/usr/bin/env bash
# run_test.sh — Automated test for drp POC (H1/H2/H3/F5)
#
# Topology:  C → B → A (linear mesh)
#            drpc → A (client registered on A)
#            local HTTP server on :5000
#
# Run:  bash poc/run_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

LOG_DIR="$SCRIPT_DIR/.logs"
mkdir -p "$LOG_DIR"

PIDS=()
PASS=0
FAIL=0

cleanup() {
	for pid in "${PIDS[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	[ -n "${BACKEND_DIR:-}" ] && rm -rf "$BACKEND_DIR" 2>/dev/null || true
}
trap cleanup EXIT

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

pass() {
	echo -e "  ${GREEN}PASS${NC} $1"
	PASS=$((PASS + 1))
}
fail() {
	echo -e "  ${RED}FAIL${NC} $1"
	FAIL=$((FAIL + 1))
}

wait_port() {
	local port=$1 max=50
	for ((i = 0; i < max; i++)); do
		if python3 -c "import socket; s=socket.socket(); s.settimeout(0.3); s.connect(('localhost', $port)); s.close()" 2>/dev/null; then
			return 0
		fi
		sleep 0.1
	done
	echo -e "  ${RED}TIMEOUT${NC} waiting for port $port"
	return 1
}

echo -e "${BOLD}=== drp POC Test ===${NC}"
echo

# -------------------------------------------------------
# 1. Local HTTP server (simulates backend)
# -------------------------------------------------------
BACKEND_DIR=$(mktemp -d)
echo "drp-poc-ok" >"$BACKEND_DIR/index.html"
LOCAL_PORT=15000
echo -e "${YELLOW}Starting local HTTP server on :${LOCAL_PORT}${NC}"
python3 -m http.server "$LOCAL_PORT" --directory "$BACKEND_DIR" >"$LOG_DIR/local.log" 2>&1 &
PIDS+=($!)
wait_port "$LOCAL_PORT"

# -------------------------------------------------------
# 2. drps servers (A-B-C linear mesh)
# -------------------------------------------------------
echo -e "${YELLOW}Starting drps-A on :8001/:9001${NC}"
python3 drps.py --node-id A --http-port 8001 --control-port 9001 -v \
	>"$LOG_DIR/drps-A.log" 2>&1 &
PIDS+=($!)
wait_port 9001

echo -e "${YELLOW}Starting drps-B on :8002/:9002 (peer→A)${NC}"
python3 drps.py --node-id B --http-port 8002 --control-port 9002 \
	--peers localhost:9001 -v >"$LOG_DIR/drps-B.log" 2>&1 &
PIDS+=($!)
wait_port 9002

echo -e "${YELLOW}Starting drps-C on :8003/:9003 (peer→B)${NC}"
python3 drps.py --node-id C --http-port 8003 --control-port 9003 \
	--peers localhost:9002 -v >"$LOG_DIR/drps-C.log" 2>&1 &
PIDS+=($!)
wait_port 9003

# Let mesh stabilize
sleep 1

# -------------------------------------------------------
# 3. drpc client (registers on A)
# -------------------------------------------------------
echo -e "${YELLOW}Starting drpc (myapp → A, local :${LOCAL_PORT})${NC}"
python3 drpc.py --server localhost:9001 --alias myapp \
	--hostname myapp.example.com --local localhost:$LOCAL_PORT -v \
	>"$LOG_DIR/drpc.log" 2>&1 &
PIDS+=($!)
sleep 1

echo
echo -e "${BOLD}--- Tests ---${NC}"

# -------------------------------------------------------
# H1: Local hit — user → A, drpc on A
# -------------------------------------------------------
STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
	-H "Host: myapp.example.com" http://localhost:8001 --max-time 5 || echo "000")
if [ "$STATUS" = "200" ]; then
	pass "H1: local hit (A) → $STATUS"
else
	fail "H1: local hit (A) → expected 200, got $STATUS"
fi

# -------------------------------------------------------
# H2: Remote 1-hop — user → B, relay B→A
# -------------------------------------------------------
STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
	-H "Host: myapp.example.com" http://localhost:8002 --max-time 10 || echo "000")
if [ "$STATUS" = "200" ]; then
	pass "H2: remote 1-hop (B→A) → $STATUS"
else
	fail "H2: remote 1-hop (B→A) → expected 200, got $STATUS"
fi

# -------------------------------------------------------
# H3: Partial mesh 2-hop — user → C, relay C→B→A
# -------------------------------------------------------
STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
	-H "Host: myapp.example.com" http://localhost:8003 --max-time 10 || echo "000")
if [ "$STATUS" = "200" ]; then
	pass "H3: partial mesh 2-hop (C→B→A) → $STATUS"
else
	fail "H3: partial mesh 2-hop (C→B→A) → expected 200, got $STATUS"
fi

# -------------------------------------------------------
# F5: Unknown host → broadcast timeout → 502
# -------------------------------------------------------
STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
	-H "Host: unknown.example.com" http://localhost:8002 --max-time 10 || echo "000")
if [ "$STATUS" = "502" ]; then
	pass "F5: unknown host → $STATUS"
else
	fail "F5: unknown host → expected 502, got $STATUS"
fi

# -------------------------------------------------------
# Summary
# -------------------------------------------------------
echo
echo -e "${BOLD}=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="

if [ "$FAIL" -gt 0 ]; then
	echo -e "\n${YELLOW}Logs in: $LOG_DIR/${NC}"
	echo "  drps-A: $LOG_DIR/drps-A.log"
	echo "  drps-B: $LOG_DIR/drps-B.log"
	echo "  drps-C: $LOG_DIR/drps-C.log"
	echo "  drpc:   $LOG_DIR/drpc.log"
fi

exit "$FAIL"
