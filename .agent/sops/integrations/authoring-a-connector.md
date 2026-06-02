# SOP: Authoring a Connector

How to add a new `sdk/integrations/<name>` connector. This is the durable
version of the extraction recipe (previously only in a task doc). Reference
connector: `gitlab` or `azuredevops` (both follow this fully). `plane` is
deliberately narrower — see [Plane is narrower](#plane-is-narrower).

---

## The `sdk/core` contract — what you must satisfy

Every connector conforms to `sdk/core`; it never extends it. If `core` lacks a
type you genuinely need, add it to `core` (and note it) rather than forking a
local copy.

| Contract | Implement when | Notes |
| --- | --- | --- |
| `Adapter` (`Name() string`) | always | returns the connector key, e.g. `"gitlab"` |
| `Pollable` (`NewPoller(PollerDeps) Poller`) | the tracker supports listing/polling issues | bridges `PollerDeps` → your connector poller |
| `WebhookCapable` (`WebhookSource() string`) | the tracker can push webhooks | |
| `Poller` (`Start(ctx) error`) | returned by `NewPoller` | the polling loop |

Add the compile-time assertions in `adapter.go`:

```go
var (
    _ core.Adapter        = (*Adapter)(nil)
    _ core.Pollable       = (*Adapter)(nil)
    _ core.WebhookCapable = (*Adapter)(nil)
)
```

### Normalized events you emit

- `core.IssueEvent` — map your native issue/work-item to this at the boundary
  (`toIssueEvent`). Fields:
  - `IssueID` — adapter-native primary ID (string).
  - `SequenceID` — **provider-prefixed** human key (`GL-42`, `AZDO-42`,
    `PLANE-42`). Feeds host branch naming; the prefix keeps IDs distinct across
    providers. Empty if the tracker has no such concept.
  - `Title`, `Body` — **sanitized** (see below).
  - `Labels` — raw label/tag names (or UUIDs for Plane).
  - `Priority` — `core.NormalizePriority(int(nativePriority))`; `""` only if the
    tracker has no priority concept.
  - `ProjectID`.
- `core.PRCreatedEvent` — emit via `PollerDeps.OnPRCreated` when you open a PR/MR.
- `core.IssueResult` — what `PollerDeps.Handler.HandleIssue` returns; map it back
  to your connector's result type.

### Host callbacks (callbacks over imports)

Never import the host. If you need host behavior, accept an interface:
`PollerDeps.Handler` (process an issue), `PollerDeps.OnPRCreated`,
`PollerDeps.ProcessedStore` (dedupe across restarts),
`core.ActiveExecutionLister` (`ListActiveTaskIDs`, for stale-branch/label
cleanup).

---

## Load-bearing rules (these are why the SDK exists)

1. **Sanitize untrusted text in the live path.** Third-party title/body is
   hostile input. Run `text.SanitizeUntrusted` (+ any HTML/template cleaning)
   in a `sanitize*` helper called from the poller/webhook **before** the item
   reaches `toIssueEvent`. `core.IssueEvent` must never carry raw text.
   - gitlab: `sanitizeIssueInPlace` mutates `Issue.Title/Description`.
   - azuredevops: `sanitizeWorkItemFields` rewrites `Fields["System.Title"]` /
     `["System.Description"]` (Azure serves description as HTML).
   - plane: `sanitizeWorkItemInPlace`.
   The helper takes the poller's injected `*slog.Logger` and warns
   (`invisible_unicode_stripped`) when runes are stripped — an attack signal.
2. **No hardcoded `slog.Default()` in leaf logic.** Constructors may accept a
   logger and default to `slog.Default()` (that's fine). Sanitize/convert
   helpers receive the injected logger.
3. **No host-domain logic.** The SDK emits normalized `IssueEvent`s; the host
   builds LLM prompts. Do not add `TaskInfo`/`BuildTaskPrompt`-style code.
4. **De-triplicate priority.** Keep your local `Priority int` enum (0=None,
   1=Urgent, 2=High, 3=Medium, 4=Low), but normalize via
   `core.NormalizePriority` — do not re-implement the int→string map.
5. **Neutral public naming.** Config trigger field is `TriggerLabel`
   (yaml `trigger_label`); default value `"pilot"`. No `Pilot*` exported names.
6. **Dependency rules.** Only the connector's own client lib + `sdk/core` +
   `sdk/util/*`. Never import Pilot or any host.

---

## File layout (full connector)

```
sdk/integrations/<name>/
  adapter.go        Adapter + NewPoller bridge + toIssueEvent
  client.go         API client (its own deps)
  types.go          Config (TriggerLabel), Priority enum, native types
  convert.go        sanitize* + native field-extraction helpers
  poller.go         polling loop; calls sanitize* before dispatch
  merger.go         PR/MR merge orchestration (if supported)
  cleanup.go        stale trigger-label cleanup (uses ActiveExecutionLister)
  webhook.go        webhook handler; calls sanitize* before callback
  *_test.go         per-file tests (incl. an ASCII-smuggling sanitize guard)
```

### Plane is narrower

`plane` has **no** `merger.go` / `cleanup.go` / `converter.go` — it offers
polling + webhooks and delegates merge/cleanup to the host. That's intentional,
not a missing piece. Use `gitlab`/`azuredevops` as the structural reference for
a full connector.

---

## Per-port acceptance gates

```bash
go build ./...                                  # green
go vet ./...                                    # clean
go test -race ./sdk/integrations/<name>/...     # green
gofmt -l sdk/                                    # no output
grep -rn "qf-studio/pilot" sdk/                  # nothing
grep -rEn "pilot_label|pilot_tag|\bPilotLabel\b|\bPilotTag\b|TaskInfo|BuildTaskPrompt" sdk/  # nothing
```

Plus: a unit test proving the `sanitize*` helper strips invisible Unicode from
title and body (see `convert_test.go` in gitlab/azuredevops).

---

## Cross-repo source (when porting from Pilot)

```bash
gh repo clone qf-studio/pilot /tmp/pilot-src -- --depth 1
# port from /tmp/pilot-src/internal/adapters/<name>/ ; do NOT commit it
```

Drop host-domain helpers during the port (prompt builders, `TaskInfo`), wire
sanitize into the live path, normalize priority via `core`, and rename the
trigger field to `TriggerLabel`.

---

## See also

- [driving-the-sdk-board](../development/driving-the-sdk-board.md) — the
  operational loop: file the 2 issues, move to `Todo` to dispatch (the daemon
  ignores the `pilot` label alone), review, manual merge, clean the board.
