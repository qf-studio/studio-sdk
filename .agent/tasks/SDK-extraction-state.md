# Studio SDK Extraction — Current State (2026-06-03)

**Source repo (extraction origin):** `qf-studio/pilot` (`~/Projects/startups/pilot`)
**Target repo (this one):** `qf-studio/studio-sdk` (`~/Projects/startups/studio-sdk`)
**Driver:** Pilot drives studio-sdk via the GH Project board "Studio SDK"
(`github.com/orgs/qf-studio/projects/1`). Auto-add workflow ON for studio-sdk
issues+PRs.

**Current tag:** `v0.25.0` — **SDK extraction COMPLETE (10/10 connectors)** + M7 executor-interface hardening.

**M7-prep audit (2026-06-03, `v0.25.0`):** audited every connector against the
three executor-implemented interfaces Pilot injects (`PRCreator`,
`SubIssueCreator`, `SubIssueLinker`). Demand = 4 wiring sites in Pilot
(`handlers.go:548` linear SubIssueCreator, `:1034` plane SubIssueCreator,
`:1145` gitlab PRCreator, `main.go:753` github SubIssueLinker). Result: 3/4
already matched; **linear had dropped `CreateIssue` in extraction → restored
(`feat(linear)`)**. jira/asana never had it (no gap); azuredevops matches
in-tree. The SDK now satisfies all three host interfaces via structural typing.
- Issue-tracker quartet: github (`v0.10/0.11`), linear (`v0.12/0.13`), jira (`v0.14/0.15`), asana (`v0.16/0.17`).
- Chat-bridge contract in `sdk/core/chat.go` (`v0.18.0`, #47).
- Chat trio: telegram (`v0.19/0.20`, #49/#50) — long-poll; slack (`v0.21/0.22`, #54/#55) — Socket Mode WS; discord (`v0.23/0.24`, #59/#60) — Gateway WS.
- Plus pre-existing on main from M1/M2: plane, gitlab, azuredevops.

**Runtime deps:** `github.com/gorilla/websocket v1.5.3` (first and only — shared by slack + discord). `sdk/{core,log,util,testutil}` remain stdlib-only.

**Releases are daemon-automated:** the Pilot daemon auto-tags a release on every
merge to main (`release: version_strategy: conventional_commits, tag_prefix: v`
in `~/.pilot/config.yaml`) → one minor bump *per merged PR*, as lightweight tags.
The driver loop does NOT `git tag`; we add annotated GitHub Releases with
changelogs on top of the daemon's tags. So each connector consumes ~2 minors.

**Daemon finalization is flaky** (carry-forward operational knowledge): the worker
commits + exits `success`, but the daemon often stalls before push/PR (hit
#29/#32/#33/#55), then either finalizes ~15-20 min late (duplicate PR — close it)
or marks the issue `pilot-retry-ready` and deletes the branch. Recovery: push the
worker commit from its worktree, open the PR, remove `pilot-retry-ready`, merge
promptly. See [[pilot-known-bugs]] + [[pilot-daemon-logs]].

---

## Connectors shipped (10 / 10)

| Connector | Milestone | PRs | Tag | Notes |
| --- | --- | --- | --- | --- |
| `plane` | M1 | #10 | `v0.x` (pre-tag) | narrower — no merger/cleanup |
| `gitlab` | M2.2 | #19, #21 | M2 | |
| `azuredevops` | M2.3 | #23, #25 | M2 | |
| `github` | M3 | #30, #31 | `v0.10/0.11` | stdlib-only (no `go-github`) |
| `linear` | M4 | #34, #37 | `v0.12/0.13` | |
| `jira` | M5 | #40, #41 | `v0.14/0.15` | |
| `asana` | M6 | #44, #45 | `v0.16/0.17` | |
| **chat contract** | — | #47 | `v0.18.0` | `sdk/core/chat.go` |
| `telegram` | — | #49, #50 | `v0.19/0.20` | long-poll; chat reference |
| `slack` | — | #54, #55 | `v0.21/0.22` | Socket Mode WS; **first ext dep** `gorilla/websocket` |
| `discord` | — | #59 (PR #61), #60 (PR #62) | `v0.23/0.24` | Gateway WS; reuses `gorilla/websocket` |

### Invariant verification (local, post-`v0.24.0`)
- `grep -rn "qf-studio/pilot" sdk/` → no matches.
- All connectors implement `sdk/core` contracts; injected loggers; no host imports.
- Only `sdk/integrations/{slack,discord}` pull `gorilla/websocket`. Core/util/log/testutil stay stdlib-only.
- `go mod tidy` is a no-op.

### Test coverage snapshot (post-`v0.24.0`, `go test -cover ./sdk/...`)
| Package | Coverage |
| --- | --- |
| `sdk/core` | 100.0% |
| `sdk/util/text` | 96.4% |
| `sdk/integrations/asana` | 85.3% |
| `sdk/integrations/jira` | 81.2% |
| `sdk/integrations/telegram` | 78.9% |
| `sdk/integrations/azuredevops` | 60.8% |
| `sdk/integrations/github` | 59.3% |
| `sdk/integrations/linear` | 58.9% |
| `sdk/integrations/slack` | 52.9% |
| `sdk/integrations/gitlab` | 49.1% |
| `sdk/integrations/discord` | 48.9% |
| `sdk/integrations/plane` | 35.9% |

The chat connectors land lower because Gateway/Socket-Mode WS loops (`transport.go`)
have network-driven branches that don't unit-test cleanly; the bridge/adapter
layers above them have direct table-driven tests. Pre-existing offenders
(`plane` 35.9%, `gitlab` 49.1%) trace to M1/M2 polling code with fewer table
cases. None blocks correctness — race detector clean, `core` is 100%.

---

## What's next (M7 — Pilot cutover, deferred)

Wire Pilot to **consume** `sdk/integrations/*` instead of `internal/adapters/*`.
Riskiest step in the program: behavior parity across the issue-tracker quartet
(`SequenceID` prefix change, `Priority` newly populated for gitlab/azuredevops,
`Body` now sanitized) and the chat trio (host still owns the task-lifecycle UX —
confirmation/progress/result, command execution, intent — the SDK only normalizes
messages + send/edit/ack).

Tracked: issue #27 (M7 coordination).

After M7 → M8 bot template (mentioned in roadmap; not started) → consider
stabilizing the public API for `v1.0.0`.

---

## The proven extraction recipe (kept for reference / future ports)

Per connector, file **2 `no-decompose` board issues** on the GH project:

1. **Client / data layer** — `client.go`, `types.go`, `converter.go`,
   `notifier.go`, `webhook.go` (+ tests) — or for chat: `client.go`, `types.go`,
   `sanitize.go`.
2. **Behavior layer** — `poller.go`, `merger.go`, `cleanup.go`, `adapter.go` —
   or for chat: `transport.go`, `bridge.go`, `adapter.go`.

Each spec stays lean (no epic keywords, ≤4 checkboxes). Each runs *direct*
~20 min → scoped PR → **manual `gh pr merge`** (large PRs hit a stage
approval-misconfig upstream pilot#2598) → daemon auto-tags → annotated GH
release → manually flip board card to Done + delete the no-status PR card
autopilot adds.

### Per-port acceptance gates
- `go build ./...` green
- `go test -race ./sdk/integrations/<name>/...` green
- `go vet ./...` clean
- `gofmt -l sdk/` empty
- `grep -rn "qf-studio/pilot" sdk/` returns nothing
- `go mod tidy` is a no-op
- No new third-party deps in `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil`
- No package-level logging — injected logger, defaults to `slog.Default()`
- For chat PRs: `var _ core.Adapter = (*Adapter)(nil)`, `var _ core.ChatCapable = (*Adapter)(nil)`; `Name()` returns the connector name; sanitize CALLED in the `Start` loop before emitting `core.MessageEvent`; commands emit `Action:"command"` with NO execution; host-domain dropped (`messenger`/`formatter`/`notifier`/`users`/`comms`/`intent`/`executor`).

### Cross-repo source access
Executor works in studio-sdk worktree; gets Pilot source via:
```
gh repo clone qf-studio/pilot /tmp/pilot-src -- --depth 1
```
Port from `/tmp/pilot-src/internal/adapters/<name>/`, then discard.
Don't commit `/tmp/pilot-src`.

---

## Operational knowledge (carry forward)

- **Board IDs:** project `PVT_kwDOD34yzs4BZIGz`; Status field `PVTSSF_lADOD34yzs4BZIGzzhUJB8o`; Todo `0b0c1283` / Done `5dbe80c0`. Local `gh` token is `read:project` only; flip cards via the daemon PAT (GraphQL) — token at `awk '/^    github:/{f=1} f&&/token:/{print $2; exit}' ~/.pilot/config.yaml`. Procedure: `sops/development/driving-the-sdk-board.md`.
- **Watcher:** `python3 ~/.claude/pilot-watch.py auto 16` (live worker stream; `daemon.log` is stale).
- **Review discipline paid off:** caught real defects across the program — #28 webhook sanitize helper present but not CALLED (prompt-injection hole), golangci `unused`/`ineffassign` on #33/#55 (CI-only, not local), out-of-scope worker scratch docs (`.agent/tasks/gh-<n>.md` — happened on #55). ALWAYS verify sanitize is actively called in the live path, watch CI lint, audit commit scope.

---

## Key references

- Chat contract spec: `.agent/system/chat-bridge-design.md`
- Architecture: `.agent/system/project-architecture.md`
- SOPs: `sops/development/driving-the-sdk-board.md`, `sops/integrations/authoring-a-connector.md`, `sops/deployment/tagging-and-release.md`
- GH project board: `https://github.com/orgs/qf-studio/projects/1`
- Releases: `https://github.com/qf-studio/studio-sdk/releases`
- Memory: `[[pilot-known-bugs]]`, `[[pilot-daemon-logs]]`, `[[driver-model]]`, `[[extraction-recipe]]`
