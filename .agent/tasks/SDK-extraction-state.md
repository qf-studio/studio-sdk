# Studio SDK Extraction — Current State (2026-06-02)

**Source repo (extraction origin):** `qf-studio/pilot` (`~/Projects/startups/pilot`)
**Target repo (this one):** `qf-studio/studio-sdk` (`~/Projects/startups/studio-sdk`)
**Driver:** Pilot drives studio-sdk via the GH Project board "Studio SDK"
(`github.com/orgs/qf-studio/projects/1`). Auto-add workflow ON for studio-sdk
issues+PRs.
**Current tag:** `v0.9.0` (origin/main, `a0108a1`).
**Local checkout:** reconciled to `origin/main` (`a9af2dd`) on 2026-06-02. The
prior diverged local fork (`455842e`, which had reworked `ActiveExecutionLister`
to `ActiveBranches` and stripped `ExecutionMode`/`MergeWaiter`/metrics tests)
was discarded in favour of origin. Two salvaged ideas (provider-prefixed
`SequenceID`, gitlab priority-from-labels) were folded into the neutralization
work below.

---

## What's done

### Foundation (M0 → M2.1)
- `sdk/core`: `Adapter`, `Pollable`, `WebhookCapable`, `Poller` interfaces;
  `IssueEvent`, `IssueResult`, `PRCreatedEvent` normalized events;
  `IssueHandler`, `ProcessedStore`, `PollerDeps`; `registry` (+ tests);
  `ActiveExecutionLister` host callback (M2.1, `9416e92`).
- `sdk/log`: minimal `Logger` interface — `*slog.Logger` satisfies it directly,
  plus `Nop`.
- `sdk/util/text`: `SanitizeUntrusted` / `SanitizeUntrustedString`
  (anti-prompt-injection — drop-in API match with Pilot's `internal/text`).
- `sdk/util/skipreason`: shared poller skip-reason constants + metrics
  interface; guard test added (`18d2a02`).
- `sdk/testutil`: fake token constants (`FakePlaneAPIKey`, etc.) for safe
  push-protected tests.
- `Makefile` (`build`/`test`/`lint`/`fmt`/`tidy`) + `sdk/doc.go`.

### Connectors extracted (3 / 10)
| Connector | Milestone | Commits / PRs |
| --------- | --------- | ------------- |
| `plane`        | M1   | `12f4f82`, PR #10 |
| `gitlab`       | M2.2 | `2bb4f67` (PR #19), `e41ab23` (PR #21) |
| `azuredevops`  | M2.3 | `d2086e1` (PR #23), `a9af2dd` (PR #25) |

All three: ported, tested, zero `qf-studio/pilot` refs in `sdk/` (verified).

### Invariant verification (local)
- `grep -rn "qf-studio/pilot" sdk/` → no matches.
- Connectors implement `sdk/core` contracts; loggers injected; only stdlib deps
  in core / util / testutil.

### Surface neutralization + security fix (2026-06-02 — MERGED PR #26, released `v0.9.0`)
Branch `refactor/sdk-neutralize-prompt-layer` rebase-merged to `main` (`a0108a1`),
tagged `v0.9.0` (annotation carries the contract/behavior/security changelog).
Applied across all 3 connectors in one pass:
- **Removed host-domain prompt layer** (`TaskInfo`, `Convert*ToTask`,
  `BuildTaskPrompt`, `ExtractAcceptanceCriteria`, `PriorityName`) — the SDK now
  emits only normalized `core.IssueEvent`; the host builds prompts.
- **Closed a latent prompt-injection gap**: gitlab/azuredevops `toIssueEvent`
  previously emitted *raw* title/body (sanitize only ran in the dead prompt
  layer). Sanitization now runs in the live poll/webhook path
  (`sanitize*` helpers, injected logger).
- **Neutralized public naming**: `Config.PilotLabel`/`PilotTag` →
  `TriggerLabel` (yaml `trigger_label`); default value still `"pilot"`.
- **De-triplicated priority**: new `core.NormalizePriority` + `core.Priority*`
  vocabulary; connectors keep their int enum, normalize at the boundary.
- **`SequenceID` provider-prefixed**: `GL-`/`AZDO-`/`PLANE-`.
  Behavior changes (flag for M7): gitlab/azuredevops `Priority` now populated;
  `Body` now sanitized/cleaned; `SequenceID` format changed.
- Docs added: `sops/integrations/authoring-a-connector.md`,
  `system/chat-bridge-design.md`, `sops/deployment/tagging-and-release.md`;
  refreshed `system/project-architecture.md` + README.

---

## What remains

### Connectors NOT yet extracted (7 / 10)
Still in Pilot at `internal/adapters/`:

- `github`
- `linear`
- `jira`
- `asana`
- `slack`     ← chat — *unproven shape*; doesn't fit issue-poller mold
- `telegram`  ← chat — same
- `discord`   ← chat — same

Issue-tracker quartet (`github`, `linear`, `jira`, `asana`) is mechanical
reuse of the proven recipe. Chat trio needs an `sdk/core` bridge designed
first.

### M7 cutover (deferred)
Wire Pilot to **consume** `sdk/integrations/*` instead of `internal/adapters/*`.
Riskiest step — behavior parity. Not started.

### M8 bot template
Mentioned in the roadmap marker; not started.

---

## The proven extraction recipe

Per connector, file **2 `no-decompose` board issues** on the GH project:

1. **Client / data layer** — `client.go`, `types.go`, `converter.go`,
   `notifier.go`, `webhook.go` (+ tests).
2. **Behavior layer** — `poller.go`, `merger.go`, `cleanup.go`, `adapter.go`
   (+ tests).

Each spec stays lean (no epic keywords, ≤4 checkboxes). Each runs *direct*
~20 min → scoped PR → **manual `gh pr merge`** (large PRs hit a stage
approval-misconfig upstream pilot#2598) → manually move card → Done + delete
the no-status PR card autopilot adds.

### Per-port acceptance gates
- `go build ./...` green
- `go test -race ./sdk/integrations/<name>/...` green
- `go vet ./...` clean
- `grep -rn "qf-studio/pilot" sdk/` returns nothing
- No new third-party deps in `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil`
- No package-level logging — injected logger, defaults to `slog.Default()`
- `*<Native>` → `sdk/core.IssueEvent` mapping doesn't silently drop fields

### Cross-repo source access
Executor works in studio-sdk worktree; gets Pilot source via:
```
gh repo clone qf-studio/pilot /tmp/pilot-src -- --depth 1
```
Port from `/tmp/pilot-src/internal/adapters/<name>/`, then discard.
Don't commit `/tmp/pilot-src`.

---

## Carry-forward from the last session marker

(`pilot/.agent/.context-markers/2026-06-01-2132_sdk-m2-complete-board-loop.md`)

1. **ROTATE `ghp_` PAT** leaked into a prior transcript (not committed; lives
   in `~/.pilot/config.yaml`, untracked). Regenerate with scopes
   `project, read:org, repo`; update config; restart daemon.
2. **Pilot core bug (P1):** epic auto-decompose **loses the child's work**
   (parent finalizes empty worktree; child branch never pushed).
   **Workaround:** apply the `no-decompose` label.
3. **Pilot core bug (ops):** large PRs blocked by stage approval-misconfig
   (`require_approval:true` + `approval.enabled:false`) — upstream pilot#2598.
4. **Pilot core bug (P2):** autopilot adds PR cards with no Status to the
   board → recurring cleanup.
5. **studio-sdk LOCAL working tree is stale/dirty** — see top of this doc.

---

## GH Project board state (verified 2026-06-02)

`gh project item-list 1 --owner qf-studio` → **17 items, all `Done`**.
No `Todo`, `In Progress`, or `In Review` items. The board is empty of
queued work; picking up M3 means filing fresh issues using the recipe
below.

Missing issue numbers visible in `gh issue list` (#13, #15, #16, #17, #19,
#21, #23, #25) were PR cards autopilot added with no Status — cleaned up
per the marker.

## Suggested next pickup

Per the marker: `github` or `linear` (mechanical recipe reuse, both still
pure issue-poller mold). Either keeps SDK M3/M4 unblocked.

After the issue-tracker quartet is done, design the chat bridge in `sdk/core`
before touching `slack`/`telegram`/`discord`.

---

## Key references

- Roadmap plan (pilot-side): `~/.claude/plans/let-s-plan-all-this-humble-wilkes.md`
- M1 spec (still the canonical template): `pilot/.agent/tasks/TASK-318-sdk-m1-plane-extraction.md`
- Watch marker (M1): `pilot/.agent/.context-markers/2026-05-29-watch-m1-sdk-extraction.md`
- Completion marker (M2): `pilot/.agent/.context-markers/2026-06-01-2132_sdk-m2-complete-board-loop.md`
- GH project board: `https://github.com/orgs/qf-studio/projects/1`
- Reference connector (most recent): `sdk/integrations/azuredevops/`
