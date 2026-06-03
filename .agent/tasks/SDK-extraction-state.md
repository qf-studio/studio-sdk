# Studio SDK Extraction — Current State (2026-06-02)

**Source repo (extraction origin):** `qf-studio/pilot` (`~/Projects/startups/pilot`)
**Target repo (this one):** `qf-studio/studio-sdk` (`~/Projects/startups/studio-sdk`)
**Driver:** Pilot drives studio-sdk via the GH Project board "Studio SDK"
(`github.com/orgs/qf-studio/projects/1`). Auto-add workflow ON for studio-sdk
issues+PRs.
**Current tag:** `v0.22.0` — issue-tracker quartet (`v0.10`–`v0.17`) + chat contract (`v0.18`) + telegram (`v0.19/0.20`) + **slack COMPLETE** (`v0.21/0.22`, #54/#55). **9/10 connectors done.** discord (#59/#60) in flight = the last. **First SDK runtime dep landed with slack: `gorilla/websocket` v1.5.3** (Socket Mode), isolated to the connector; `sdk/{core,log,util,testutil}` stay stdlib-only. discord reuses the same dep (Gateway WS).
**Releases are daemon-automated:** the Pilot daemon auto-tags a release on every
merge to main (`release: version_strategy: conventional_commits, tag_prefix: v`
in `~/.pilot/config.yaml`) → one minor bump *per merged PR*, as lightweight tags.
The driver loop does NOT `git tag`; we add annotated GitHub Releases with
changelogs on top of the daemon's tags. So each connector consumes ~2 minors.
**Daemon finalization is flaky:** the worker commits + exits `success`, but the
daemon often stalls before push/PR (hit #29/#32/#33), then either finalizes
~15-20 min late (duplicate PR — close it) or marks the issue `pilot-retry-ready`
and deletes the branch (auto-closing the recovery PR). Recovery: push the worker
commit from its worktree, open the PR, remove `pilot-retry-ready`, merge promptly
(merging closes the issue → kills the retry). See [[pilot-daemon-logs]].
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

### Connectors extracted (5 / 10)
| Connector | Milestone | Commits / PRs |
| --------- | --------- | ------------- |
| `plane`        | M1   | `12f4f82`, PR #10 |
| `gitlab`       | M2.2 | `2bb4f67` (PR #19), `e41ab23` (PR #21) |
| `azuredevops`  | M2.3 | `d2086e1` (PR #23), `a9af2dd` (PR #25) |
| `github`       | M3   | PR #30 (#28, v0.10.0), PR #31 (#29, v0.11.0) |
| `linear`       | M4   | PR #34 (#32, v0.12.0), PR #37 (#33, v0.13.0) |
| `jira`         | M5   | PR #40 (#38, v0.14.0), PR #41 (#39, v0.15.0) |
| `asana`        | M6   | PR #44 (#42, v0.16.0), PR #45 (#43, v0.17.0) |

All seven: ported, tested, zero `qf-studio/pilot` refs in `sdk/` (verified).
`linear`/`jira`/`asana` are narrower (no merger/cleanup; host-delegated); stdlib-only.
`jira`/`asana` required dropping the host prompt layer (`TaskInfo`/`ConvertToTaskInfo`/
`BuildTaskPrompt`) while preserving sanitize in the live path — same as PR #26.
**Issue-tracker quartet (github→linear→jira→asana) COMPLETE @ v0.17.0.**

### GitHub connector (M3) — SHIPPED (2026-06-02, v0.11.0)
Stdlib-only after all (uses `net/http`, **no** `go-github` dep — the expected
external dep never materialized; `go.mod` stays require-less, no `go.sum`).
**Ported:** `client`/`types`/`converter`/`notifier`/`webhook`/`retry`/
`approval_config` (#28) then `poller`/`merger`/`cleanup`/`adapter` (#29).
**Dropped** (Pilot-domain): `grouping`/`issue_create`/`project_board`/
`spec_validator`.

| Issue | Scope | PR | Release | Notes |
| --- | --- | --- | --- | --- |
| #28 | client/data (+retry, approval_config) | #30 | v0.10.0 | reviewer patched a missing webhook sanitize call before merge |
| #29 | poller/merger/cleanup/adapter | #31 | v0.11.0 | daemon stalled before push; commit `56eb419` recovered + pushed manually; gofmt fixup |

Both reviewed against the gates (sanitize wired in live poll/webhook paths,
contract assertions, no pilot imports). Dispatch via the board per
[driving-the-sdk-board](../sops/development/driving-the-sdk-board.md).

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

### Chat phase (3 / 10 remaining)

**Chat-bridge contract LANDED** in `sdk/core` (`chat.go`, #47, **v0.18.0**):
`MessageEvent`/`Identity`/`OutboundMessage`/`Button`/`MessageRef` +
`MessageHandler`/`ChatCapable`/`ChatBridge`/`ChatDeps` — a thin parallel surface
to the issue contract (approved spec in `system/chat-bridge-design.md`; outbound
= Text + Buttons; inbound = `ChatBridge.Start(ctx)` listener). Stdlib-only.

- `telegram`  ← **DONE** (`v0.19/0.20`, #49/#50) — reference; long-poll
- `slack`     ← **DONE** (`v0.21/0.22`, #54/#55) — Socket Mode WS; first ext dep
- `discord`   ← **IN FLIGHT** (#59/#60) — Gateway WS; reuses gorilla/websocket; LAST connector

**CHECKPOINT (2026-06-03):** the telegram reference validated the chat contract
end-to-end (`ChatBridge.Start/Send/Edit/Ack`, message/command/callback mapping,
inbound sanitize in the Start loop, Buttons→inline keyboard). slack/discord are
NOT mechanical — heavier delivery (Socket Mode / Gateway). **Awaiting operator
go-ahead before porting them.** Recipe when resumed: same 2-issue split
(client/data+bridge), drop Pilot host-domain (intent/executor/memory/briefs/
transcription, command execution → emit `Action:"command"`), config = `BotToken`
+ allow-list, sanitize inbound text in the live listen path.

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

## GH Project board state (verified 2026-06-02, M3 kickoff)

`gh project item-list 1 --owner qf-studio` → 17 `Done`, plus M3 work:
- **#28** `In Progress` (daemon building the github client/data PR).
- **#29** `NONE` + `backlog` (held behind #28).
- **#26** `NONE` — stale merged-PR card (autopilot leftover, safe to delete).
- **#27** `NONE` — M7 coordination note, intentionally open.

**Intake gate (learned 2026-06-02):** the daemon dispatches from board
`Status=Todo`, **not** the `pilot` label. A freshly-filed issue sits at
`NONE` and Pilot stays idle until the card is moved to `Todo`. The local `gh`
token (`read:project`) can't flip it — use the daemon PAT (GraphQL) or the
dashboard. Full procedure + IDs:
[driving-the-sdk-board](../sops/development/driving-the-sdk-board.md).

## Suggested next pickup

In flight: `github` (M3). After it merges + tags `v0.10.0`, continue the
quartet: `linear` → `jira` → `asana` (mechanical recipe reuse).

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
