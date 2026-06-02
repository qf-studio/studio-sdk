# PR Review Checklist — #28 (github client/data)

**Pre-staged:** 2026-06-02, while daemon was mid-build (no PR open yet).
**Issue:** #28 `feat(sdk/integrations/github): port client, types, converter, notifier, webhook`
**Scope guard from the issue:** touch ONLY `sdk/integrations/github/`,
`sdk/testutil/tokens.go`, top-level `go.mod`/`go.sum`. Nothing else.
**Merge:** manual `gh pr merge 28 -R qf-studio/studio-sdk --rebase --delete-branch`
(large PRs hit the upstream stage approval-misconfig, pilot#2598).

---

## Expected file manifest (`sdk/integrations/github/`, package `github`)

**Must be PRESENT (7 + tests):**
`client.go`, `types.go`, `converter.go`, `notifier.go`, `webhook.go`,
`retry.go`, `approval_config.go` (+ their table-driven `*_test.go`).

**Must be ABSENT:**
- `poller.go`, `merger.go`, `cleanup.go`, `adapter.go` → these are **#29**.
- **`gh2432_test.go`** → its tests (`TestPoller_RetryLabel_*`) call
  `findOldestUnprocessedIssue` (poller-internal). Can't compile without the
  poller → **belongs with #29, not here.** Flag if present.
- `grouping.go`, `issue_create.go`, `project_board.go`, `spec_validator.go`
  → Pilot-domain, **dropped entirely** (epic `Parent: GH-NNN`,
  `CreatePilotIssue`/`PILOT_*` env/`executor.RepoAllowlist`, board status flips,
  `<!-- pilot-spec-incomplete -->` marker). Must NOT appear in `sdk/`.

---

## Gates to run against the PR branch

```bash
# fetch + check out the daemon branch first, then:
go build ./...                                   # green
go vet ./...                                     # clean
go test -race ./sdk/integrations/github/...      # green
gofmt -l sdk/                                     # no output

grep -rn "qf-studio/pilot" sdk/                   # NOTHING
grep -rn "internal/logging\|logging\." sdk/integrations/github/   # NOTHING (use injected *slog.Logger)
grep -rEn "pilot_label|pilot_tag|\bPilotLabel\b|\bPilotTag\b|TaskInfo|BuildTaskPrompt|CreatePilotIssue|PILOT_" sdk/  # NOTHING
grep -rn "grouping\|project_board\|spec_validator\|issue_create" sdk/integrations/github/  # NOTHING (dropped files)

# go.mod / go.sum (FIRST external dep in the repo — go-github):
test -f go.sum && echo "go.sum present ✓"
go mod tidy && git diff --exit-code go.mod go.sum   # tidy must be a NO-OP
```

## Contract / load-bearing review (manual read)

- [ ] **Sanitize in the live path.** `converter.go`/`webhook.go` must run
  `text.SanitizeUntrusted` (via a `sanitize*` helper) on title/body **before**
  building any `core.IssueEvent`. `core.IssueEvent` must never carry raw text.
  (This is the v0.9.0 security invariant — the whole reason the SDK exists.)
  - Note: full `toIssueEvent` lands in #29's `adapter.go`, but the sanitize
    helper + its unit test (invisible-Unicode strip on title AND body) should
    already be here with the converter. Confirm the guard test exists.
- [ ] **Injected logger, no `slog.Default()` in leaf logic.** Constructors may
  default to `slog.Default()` via a `WithLogger` option; sanitize/convert
  helpers receive the injected `*slog.Logger`.
- [ ] **Reuse, don't duplicate** `sdk/util/text` and `sdk/util/skipreason` if a
  ported file references them.
- [ ] **Priority** normalized via `core.NormalizePriority` (keep local int enum,
  don't re-implement the int→string map).
- [ ] **Neutral naming.** Config trigger field is `TriggerLabel`
  (yaml `trigger_label`), default `"pilot"`. No `Pilot*` exported names.
- [ ] **testutil tokens.** `sdk/testutil/tokens.go` gains
  `FakeGitHubToken = "test-github-token"` and
  `FakeGitHubWebhookSecret = "test-github-webhook-secret"`. Tests use ONLY these
  — no realistic secret strings (push-protection + secret-patterns CI check).
- [ ] **Dependency hygiene.** `go.mod` require block adds only the go-github
  line(s); `sdk/{core,log,util,testutil}` still import stdlib only.

## After merge
1. `gh pr merge 28 -R qf-studio/studio-sdk --rebase --delete-branch`.
2. Board: move #28 card → **Done**; delete the no-status PR card autopilot adds.
3. Then unblock **#29**: flip card → `Todo` (daemon-PAT GraphQL) **and**
   `gh issue edit 29 -R qf-studio/studio-sdk --remove-label backlog --add-label pilot`.
   (Procedure + IDs: `sops/development/driving-the-sdk-board.md`.)

## Reference state at pre-stage time
- `go.mod`: module-only, `go 1.24.2`, **no require block, no go.sum yet**.
- `sdk/integrations/github/`: absent (expected).
- Reference connector to mirror: `sdk/integrations/azuredevops/` (most recent full connector).
- Pilot source cached at `/tmp/pilot-src/internal/adapters/github/` (29 files).
