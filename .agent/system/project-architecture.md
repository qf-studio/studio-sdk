# Studio SDK — Project Architecture

**Updated**: 2026-06-03

## Purpose

Reusable Go connectors for Studio projects — issue trackers (GitHub, GitLab,
Azure DevOps, Linear, Jira, Asana, Plane) and chat platforms (Slack, Telegram,
Discord). Extracted from Pilot so any Studio Go service can consume the same
connectors without taking on Pilot itself as a dependency. **All 10 connectors
shipped (`v0.24.0`); next step is M7 — wiring Pilot to consume the SDK.**

## Module

- Module path: `github.com/qf-studio/studio-sdk`
- Go: `1.24.2`
- Stability: `v0.x` (unstable; breaking changes allowed until `v1.0.0`)

## Layout

```
sdk/
  core/            Adapter contracts, registry, normalized event types,
                   chat contract (chat.go: MessageEvent/OutboundMessage/
                   ChatCapable/ChatBridge)
  log/             Logger interface (a *slog.Logger satisfies it directly)
  testutil/        Shared test fakes (fake tokens, etc.)
  util/
    skipreason/    Shared poller skip-reason constants + metrics interface
    text/          Untrusted-text sanitization (prompt-injection defense)
  integrations/    One package per connector
    asana/         issue tracker
    azuredevops/   issue tracker
    discord/       chat (Gateway WS, gorilla/websocket)
    github/        issue tracker (stdlib-only)
    gitlab/        issue tracker
    jira/          issue tracker
    linear/        issue tracker
    plane/         issue tracker (narrower: no merger/cleanup, see SOP)
    slack/         chat (Socket Mode WS, gorilla/websocket)
    telegram/      chat (long-poll, stdlib-only)
  doc.go           Package-level docs
```

All 10 connectors are extracted as of `v0.24.0`. The chat trio implements the
chat contract in `sdk/core/chat.go` (see `system/chat-bridge-design.md`).
To add a future connector, follow `sops/integrations/authoring-a-connector.md`.

## Contracts (sdk/core)

The core defines a single contract surface that every connector conforms to:

**Issue-tracker contract**:
- **Adapter interfaces**: `Adapter`, `Pollable`, `WebhookCapable`, `Poller`
- **Normalized events**: `IssueEvent`, `IssueResult`, `PRCreatedEvent`
- **Priority vocabulary**: `core.NormalizePriority(rank int) string` + the
  `PriorityNone/Urgent/High/Medium/Low` constants. Connectors keep their own
  `Priority int` enum and normalize at the `IssueEvent` boundary — they do not
  each re-implement the int→string mapping.
- **Registry**: lets hosts register/lookup adapters by name
- **Host callbacks**: `ActiveExecutionLister` (`ListActiveTaskIDs`) is the
  canonical pattern. Connectors accept these as interfaces; they never import
  the host.

**Chat contract** (`sdk/core/chat.go`, since `v0.18.0`):
- **`ChatCapable`** — connector capability marker: `NewChatBridge(ChatDeps) ChatBridge`.
- **`ChatBridge`** — lifecycle: `Start(ctx)` listens, normalizes platform events
  into `core.MessageEvent`, calls `ChatDeps.HandleMessage`. `Send`/`Edit`/`Ack`
  for outbound and acknowledgment.
- **Normalized events**: `MessageEvent` (Action `"message"`/`"command"`/`"callback"`,
  ChannelID/ThreadID/MessageID, Text, Command/Args, Sender Identity), `Identity`,
  `OutboundMessage` (Text + `Buttons []Button`), `MessageRef`.
- **Inbound sanitization is mandatory**: the connector's `Start` loop runs the
  package-local `sanitizeMessageText` over inbound Text **before** emitting
  `MessageEvent` (defense against invisible-Unicode prompt injection — same
  rule as the issue-tracker poll/webhook path).
- **Commands are normalized, not executed**: the bridge emits `Action:"command"`
  with `Command`/`Args` populated; running the command is host-domain.

When you change `sdk/core`, every connector in `sdk/integrations/*` must still
compile and pass tests.

### Boundary conventions every connector follows

- **`SequenceID` is provider-prefixed**: `GL-<iid>` (GitLab), `AZDO-<id>`
  (Azure DevOps), `PLANE-<seq>` (Plane). It feeds host branch naming, so the
  prefix keeps IDs distinct across providers.
- **Untrusted text is sanitized in the live path.** The poll/webhook path runs
  the connector's `sanitize*` helper (passing the injected logger) **before**
  the work item reaches `toIssueEvent`/`core.IssueEvent`. `sdk/core` must never
  receive raw third-party title/body. The host-facing data is the normalized
  `IssueEvent` — the SDK does not build LLM prompts (that is host-domain).
- **No host-named public API.** The trigger label/tag config field is
  `TriggerLabel` (yaml `trigger_label`) across all connectors; the runtime
  default value stays `"pilot"`.

## Dependency rules

- `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil` → **stdlib only.**
- `sdk/integrations/<name>` → may depend on that connector's official client
  library, plus `sdk/core` + `sdk/util/*`. Nothing else.
- Nothing in the SDK depends on Pilot.

**Current runtime dep inventory** (as of `v0.24.0`):
`github.com/gorilla/websocket v1.5.3` — used by `slack` (Socket Mode) and
`discord` (Gateway). It's the only third-party runtime dep in the SDK.
`telegram` and every issue-tracker connector remain stdlib-only.

## Build / test / lint

Driven by `Makefile`:

- `make build` — `go build ./...`
- `make test`  — `go test -race ./...`
- `make lint`  — `go vet ./...` + `golangci-lint run` (if installed)
- `make fmt`   — `gofmt -w .`
- `make tidy`  — `go mod tidy`

## In-flight work

Extraction from Pilot is **complete** (`v0.24.0`, all 10 connectors). State
lives in `tasks/SDK-extraction-state.md`. Next: **M7 — Pilot cutover**, wiring
Pilot to consume `sdk/integrations/*` instead of its in-repo `internal/adapters/*`
(tracked at issue #27). Behavior-parity risks for M7: `SequenceID` is now
provider-prefixed; gitlab/azuredevops `Priority` is newly populated; gitlab/
azuredevops `Body` is sanitized. For the chat trio, the SDK only normalizes
messages + send/edit/ack; the host still owns the task-lifecycle UX
(confirmation/progress/result, command execution, intent).
