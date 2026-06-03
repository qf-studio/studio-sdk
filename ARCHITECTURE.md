# Architecture

Studio SDK is a set of Go connectors that normalize third-party issue trackers
and chat platforms into a single contract surface, so a host service can consume
any of them the same way. This document describes that surface and the
conventions every connector follows.

## Module

- Module path: `github.com/qf-studio/studio-sdk`
- Go: `1.24+`
- Stability: `v0.x` — the public API is unstable; breaking changes are allowed
  until `v1.0.0` and are called out in release notes.

## Layout

```
sdk/
  core/            Adapter + chat contracts, registry, normalized event types,
                   priority vocabulary (chat contract in core/chat.go)
  log/             Logger interface (a *slog.Logger satisfies it directly)
  testutil/        Shared test fakes (obviously-fake tokens, etc.)
  util/
    skipreason/    Shared poller skip-reason constants + metrics interface
    text/          Untrusted-text sanitization (prompt-injection defense)
  integrations/    One package per connector
    asana/  azuredevops/  discord/  github/  gitlab/
    jira/   linear/       plane/    slack/   telegram/
  doc.go           Package-level docs
```

## Design principles

- **Zero host dependency.** `sdk/core`, `sdk/log`, `sdk/util/*`, and
  `sdk/testutil` depend only on the Go standard library. A connector may pull its
  own client library; nothing in the SDK imports a consuming application.
- **One contract surface.** `sdk/core` owns the interfaces and event types.
  Connectors conform to them; they do not extend them.
- **Callbacks over imports.** When a connector needs host behavior (active-
  execution listing, sub-issue creation, metrics), it accepts an interface the
  host implements — it never imports the host. `ActiveExecutionLister` is the
  canonical example.
- **Untrusted text is hostile input.** Anything from a third-party API (issue
  title, PR body, chat message) passes through `sdk/util/text` in the live
  poll / webhook / listener path before it can reach an LLM or downstream system.

## Contracts (`sdk/core`)

### Issue-tracker contract
- **Adapter interfaces**: `Adapter` (`Name()`), `Pollable`, `WebhookCapable`,
  `Poller`.
- **Normalized events**: `IssueEvent`, `IssueResult`, `PRCreatedEvent`.
- **Priority vocabulary**: `core.NormalizePriority(rank int) string` plus the
  `PriorityNone/Urgent/High/Medium/Low` constants. Connectors keep their own
  `Priority int` enum and normalize at the `IssueEvent` boundary — they do not
  each re-implement the int→string mapping.
- **Registry**: lets hosts register and look up adapters by name.
- **Host callbacks**: `IssueHandler`, `ProcessedStore`, `ActiveExecutionLister`.

### Chat contract (`sdk/core/chat.go`)
- **`ChatCapable`** — connector capability: `NewChatBridge(ChatDeps) ChatBridge`.
- **`ChatBridge`** — lifecycle: `Start(ctx)` listens, normalizes platform events
  into `core.MessageEvent`, and calls `ChatDeps.HandleMessage`; `Send` / `Edit` /
  `Ack` handle outbound and acknowledgment.
- **Normalized events**: `MessageEvent` (Action `"message"` / `"command"` /
  `"callback"`, with ChannelID/ThreadID/MessageID, Text, Command/Args, Sender
  Identity), `Identity`, `OutboundMessage` (Text + `Buttons []Button`),
  `MessageRef`.
- **Inbound sanitization is mandatory**: the `Start` loop runs the connector's
  `sanitizeMessageText` over inbound text *before* emitting a `MessageEvent`.
- **Commands are normalized, not executed**: the bridge emits `Action:"command"`
  with `Command`/`Args` populated; running the command is the host's job.

When you change `sdk/core`, every connector in `sdk/integrations/*` must still
compile and pass tests.

## Boundary conventions every connector follows

- **`SequenceID` is provider-prefixed** — `GL-<iid>` (GitLab), `AZDO-<id>`
  (Azure DevOps), `PLANE-<seq>` (Plane), `PROJ-<n>` (Jira), etc. It feeds host
  branch naming, so the prefix keeps IDs distinct across providers.
- **Untrusted text is sanitized in the live path.** The poll/webhook path runs
  the connector's `sanitize*` helper (with the injected logger) before the work
  item reaches `toIssueEvent` / `core.IssueEvent`. `sdk/core` never receives raw
  third-party title/body; the SDK does not build LLM prompts (that is host-domain).
- **No host-named public API.** The trigger label/tag config field is
  `TriggerLabel` (yaml `trigger_label`) across all connectors; its default value
  is `"pilot"`.

## Dependency rules

- `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil` → **stdlib only.**
- `sdk/integrations/<name>` → may depend on that connector's official client
  library, plus `sdk/core` + `sdk/util/*`. Nothing else.
- Nothing in the SDK depends on a consuming application.

The only third-party runtime dependency in the SDK today is
`github.com/gorilla/websocket` (used by `slack` Socket Mode and `discord`
Gateway). Every other connector is stdlib-only at runtime.
