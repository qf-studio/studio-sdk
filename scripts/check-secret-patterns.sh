#!/bin/bash
# Check for realistic-looking secret patterns across all tracked files.
#
# Enforces the SDK's load-bearing "no realistic-looking tokens" rule: GitHub
# push protection blocks branches that contain real-looking credentials, so all
# test fixtures must use obviously-fake tokens (see sdk/testutil/tokens.go).
#
# Used by CI (.github/workflows/ci.yml job "secret-patterns") and `make check-secrets`.

set -e

echo "Scanning all tracked files for realistic secret patterns..."

# Patterns that look like real secrets (will trigger GitHub push protection).
# Add new patterns here when GitHub adds support for new providers.
PATTERNS=(
    'xoxb-[0-9]{10,}-[0-9]{10,}'                  # Slack bot token
    'xoxa-[0-9]{10,}-[0-9]{10,}'                  # Slack app token
    'xoxp-[0-9]{10,}-[0-9]{10,}'                  # Slack user token
    'sk-[a-zA-Z0-9]{32,}'                         # OpenAI / Anthropic-style API key
    'ghp_[a-zA-Z0-9]{36}'                         # GitHub PAT
    'gho_[a-zA-Z0-9]{36}'                         # GitHub OAuth token
    'ghu_[a-zA-Z0-9]{36}'                         # GitHub user-to-server token
    'ghs_[a-zA-Z0-9]{36}'                         # GitHub server-to-server token
    'github_pat_[a-zA-Z0-9]{22}_[a-zA-Z0-9]{59}'  # GitHub fine-grained PAT
    'AKIA[0-9A-Z]{16}'                            # AWS access key ID
    'lin_api_[a-zA-Z0-9]{40}'                     # Linear API key
)

# Files allowlisted from the scan because they intentionally contain secret
# patterns (e.g. this script defines them). DO NOT add files here without a
# clear "this is intentional/educational content" justification — every entry
# weakens the check. Paths are matched as exact lines against `git ls-files`
# output (repo-root-relative, no leading "./").
ALLOWLIST=(
    'scripts/check-secret-patterns.sh'  # this script literally lists the patterns it detects
)

# Build a temp file of files to scan: tracked files minus the allowlist.
TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT
git ls-files | grep -vFx -f <(printf '%s\n' "${ALLOWLIST[@]}") > "$TMPFILE"

# Sanity: bail loudly if the file list is suspiciously short — the allowlist
# probably matched too much, or `git ls-files` returned nothing (broken cwd).
SCAN_COUNT=$(wc -l < "$TMPFILE" | tr -d ' ')
if [ "$SCAN_COUNT" -lt 30 ]; then
    echo "❌ ERROR: only $SCAN_COUNT files queued for scan — allowlist or cwd misconfigured"
    exit 2
fi

FOUND_SECRETS=0

for pattern in "${PATTERNS[@]}"; do
    # xargs respects ARG_MAX and splits the file list if necessary.
    MATCHES=$(xargs grep -HnE "$pattern" < "$TMPFILE" 2>/dev/null || true)
    if [ -n "$MATCHES" ]; then
        echo ""
        echo "❌ ERROR: Found realistic-looking secret pattern"
        echo "   Pattern: $pattern"
        echo ""
        echo "$MATCHES" | head -10
        echo ""
        FOUND_SECRETS=1
    fi
done

if [ $FOUND_SECRETS -eq 1 ]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CI CHECK FAILED: Realistic secret patterns detected"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "GitHub's push protection will block these patterns."
    echo ""
    echo "Instead, use obviously fake tokens:"
    echo "  ✅ test-plane-api-key"
    echo "  ✅ test-gitlab-token"
    echo "  ✅ fake-api-key"
    echo ""
    echo "Or use the constants in sdk/testutil/tokens.go:"
    echo "  import \"github.com/qf-studio/studio-sdk/sdk/testutil\""
    echo "  token := testutil.FakePlaneAPIKey"
    echo ""
    echo "If the match is intentional educational content (showing what NOT"
    echo "to use), add the file to the ALLOWLIST in scripts/check-secret-patterns.sh."
    echo ""
    exit 1
fi

echo "✓ No realistic secret patterns found in $SCAN_COUNT scanned files"
exit 0
