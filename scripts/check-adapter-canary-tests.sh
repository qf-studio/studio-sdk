#!/bin/bash
# Check that each integration package with an HTTP client has a credential
# leak test.
#
# A directory below the integrations directory whose non-test Go files contain
# http.Client or http.NewRequest must have a test file in the same directory
# that calls testutil.AssertNoCanaryLeak(t as a statement. The call must start
# its line (after indentation): a mention in a // comment, in a /* */ comment
# or in a string does not count.
#
# ADAPTER_CANARY_ROOT overrides the directory to check (used by the self-test
# scripts/test-check-adapter-canary.sh).
#
# Used by CI (.github/workflows/ci.yml job "adapter-canary") and
# `make check-adapter-canary`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="${ADAPTER_CANARY_ROOT:-$REPO_ROOT/sdk/integrations}"

if [ ! -d "$ROOT" ]; then
    echo "❌ ERROR: integrations directory not found: $ROOT"
    exit 2
fi

CLIENT_PATTERN='http\.Client|http\.NewRequest'
CALL_PATTERN='^[[:space:]]*testutil\.AssertNoCanaryLeak\(t[,)]'

CHECKED=0
MISSING=()

while IFS= read -r dir; do
    shopt -s nullglob
    src=()
    tests=()
    for f in "$dir"/*.go; do
        case "$f" in
            *_test.go) tests+=("$f") ;;
            *) src+=("$f") ;;
        esac
    done
    shopt -u nullglob

    [ ${#src[@]} -eq 0 ] && continue
    grep -qE "$CLIENT_PATTERN" "${src[@]}" || continue

    CHECKED=$((CHECKED + 1))
    if [ ${#tests[@]} -eq 0 ] || ! grep -qE "$CALL_PATTERN" "${tests[@]}"; then
        MISSING+=("${dir#"$REPO_ROOT"/}")
    fi
done < <(find "$ROOT" -type d | sort)

if [ ${#MISSING[@]} -gt 0 ]; then
    echo "❌ ERROR: integration packages with an HTTP client and no credential leak test:"
    for d in "${MISSING[@]}"; do
        echo "   $d"
    done
    echo ""
    echo "Add a test in each package that calls testutil.AssertNoCanaryLeak(t, ...)"
    echo "on the error texts the client returns (see sdk/testutil)."
    exit 1
fi

echo "✓ All $CHECKED integration packages with an HTTP client have a credential leak test"
exit 0
