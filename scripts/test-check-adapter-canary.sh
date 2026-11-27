#!/bin/bash
# Self-test for scripts/check-adapter-canary-tests.sh.
#
# Builds temporary integration directories and proves four cases:
#   1. a package with a client and a real call passes;
#   2. a package with a client and no test fails and is named;
#   3. a package with a client and the call only in a comment (or a string)
#      fails and is named;
#   4. a package with no client is not checked.
#
# Used by CI (.github/workflows/ci.yml job "adapter-canary") and
# `make check-adapter-canary`.

set -euo pipefail

GATE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-adapter-canary-tests.sh"

TMPDIR_ROOT=$(mktemp -d)
trap 'rm -rf "$TMPDIR_ROOT"' EXIT

FAILURES=0

fail() {
    echo "❌ FAIL: $1"
    FAILURES=$((FAILURES + 1))
}

pass() {
    echo "✓ PASS: $1"
}

# client_pkg DIR writes a non-test Go file with an HTTP client into DIR.
client_pkg() {
    mkdir -p "$1"
    cat > "$1/client.go" <<'GO'
package pkg

import "net/http"

type Client struct {
	httpClient *http.Client
}
GO
}

# real_call DIR writes a test file into DIR that calls the leak helper.
real_call() {
    cat > "$1/client_test.go" <<'GO'
package pkg

import (
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestNoLeak(t *testing.T) {
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, "x")
}
GO
}

# run_gate ROOT runs the gate on ROOT; sets OUT and CODE.
run_gate() {
    set +e
    OUT=$(ADAPTER_CANARY_ROOT="$1" bash "$GATE" 2>&1)
    CODE=$?
    set -e
}

# Case 1: client + real call passes.
root="$TMPDIR_ROOT/case1"
client_pkg "$root/good"
real_call "$root/good"
run_gate "$root"
if [ "$CODE" -eq 0 ] && grep -q "All 1 integration packages" <<<"$OUT"; then
    pass "a package with a client and a real call passes"
else
    fail "a package with a client and a real call passes (exit $CODE): $OUT"
fi

# Case 2: client + no test fails and is named.
root="$TMPDIR_ROOT/case2"
client_pkg "$root/good"
real_call "$root/good"
client_pkg "$root/notest"
run_gate "$root"
if [ "$CODE" -eq 1 ] && grep -q "/notest$" <<<"$OUT" && ! grep -q "/good$" <<<"$OUT"; then
    pass "a package with a client and no test fails and is named"
else
    fail "a package with a client and no test fails and is named (exit $CODE): $OUT"
fi

# Case 3: client + call only in a comment (and in a string) fails and is named.
root="$TMPDIR_ROOT/case3"
client_pkg "$root/commented"
cat > "$root/commented/client_test.go" <<'GO'
package pkg

import "testing"

func TestNoLeak(t *testing.T) {
	// testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, "x")
	/* testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, "x") */
	_ = "testutil.AssertNoCanaryLeak(t, testutil.CanaryToken)"
}
GO
run_gate "$root"
if [ "$CODE" -eq 1 ] && grep -q "/commented$" <<<"$OUT"; then
    pass "a package with a client and the call only in a comment fails and is named"
else
    fail "a package with a client and the call only in a comment fails and is named (exit $CODE): $OUT"
fi

# Case 4: no client is not checked.
root="$TMPDIR_ROOT/case4"
client_pkg "$root/good"
real_call "$root/good"
mkdir -p "$root/noclient"
cat > "$root/noclient/types.go" <<'GO'
package pkg

type Event struct{ ID string }
GO
run_gate "$root"
if [ "$CODE" -eq 0 ] && grep -q "All 1 integration packages" <<<"$OUT" && ! grep -q "noclient" <<<"$OUT"; then
    pass "a package with no client is not checked"
else
    fail "a package with no client is not checked (exit $CODE): $OUT"
fi

if [ "$FAILURES" -gt 0 ]; then
    echo ""
    echo "❌ $FAILURES self-test case(s) of check-adapter-canary-tests.sh failed"
    exit 1
fi

echo ""
echo "✓ All 4 self-test cases of check-adapter-canary-tests.sh passed"
exit 0
