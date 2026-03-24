#!/bin/bash
# 集成测试脚本

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
TMP_DIR="${TMPDIR:-/tmp}"
LOG_FILE="$TMP_DIR/pg-deploy-test.log"

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

run_check() {
    local name="$1"
    shift

    echo -n "Testing $name... "
    if "$@" >"$LOG_FILE" 2>&1; then
        echo -e "${GREEN}PASSED${NC}"
        return 0
    fi

    echo -e "${RED}FAILED${NC}"
    cat "$LOG_FILE"
    return 1
}

cd "$PROJECT_ROOT"
mkdir -p .gocache

echo "=== PostgreSQL Deployment Tool - Integration Test ==="
echo ""
echo "Step 1: Run test suite"
echo "---"

FAILED_TESTS=0
if ! run_check "go test ./..." env GOCACHE="$PROJECT_ROOT/.gocache" go test ./...; then
    FAILED_TESTS=$((FAILED_TESTS + 1))
fi

echo ""
echo "Step 2: Race condition tests"
echo "---"
if ! run_check "credentials (race detection)" env GOCACHE="$PROJECT_ROOT/.gocache" go test -race ./pkg/credentials/...; then
    FAILED_TESTS=$((FAILED_TESTS + 1))
fi

echo ""
echo "Step 3: Build verification"
echo "---"
if ! run_check "compilation" env GOCACHE="$PROJECT_ROOT/.gocache" go build -o "$TMP_DIR/pg-deploy-test" ./main.go; then
    FAILED_TESTS=$((FAILED_TESTS + 1))
fi

echo ""
echo "Step 4: Code coverage"
echo "---"
if env GOCACHE="$PROJECT_ROOT/.gocache" go test -coverprofile=coverage.out ./pkg/... >/dev/null 2>&1; then
    if coverage=$(env GOCACHE="$PROJECT_ROOT/.gocache" go tool cover -func=coverage.out | tail -1 | awk '{print $3}'); then
        echo -e "${GREEN}Code coverage: $coverage${NC}"
    else
        echo "Coverage profile generated, but summary rendering was skipped"
    fi
    rm -f coverage.out
else
    echo "Coverage skipped"
fi

rm -f "$TMP_DIR/pg-deploy-test" "$LOG_FILE"

echo ""
echo "=== Test Summary ==="
if [ "$FAILED_TESTS" -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
fi

echo -e "${RED}$FAILED_TESTS test(s) failed${NC}"
exit 1
