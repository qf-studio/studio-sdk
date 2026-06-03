# Studio SDK

Reusable Go connectors for Studio projects — issue trackers (GitHub, GitLab,
Azure DevOps, Linear, Jira, Asana, Plane) and chat platforms (Slack, Telegram,
Discord), extracted from Pilot so any Studio Go service can consume them.

> **Status:** `v0.24.0` — all 10 connectors shipped. Public API is still `v0.x`
> (breaking changes allowed until `v1.0.0`).
> **Distribution:** internal to Studio. Not published publicly.

## Design principles

- **Zero Pilot dependency.** The SDK depends only on the Go standard library
  and a small set of per-connector client deps (see below). Consuming
  applications wire their own services (scheduler, persistence, metrics, LLM
  runner) in via the interfaces and callbacks declared in `sdk/core`.
- **One contract surface.** `sdk/core` owns the issue-tracker contract
  (`Adapter`, `Pollable`, `WebhookCapable`, `Poller`, `IssueEvent`,
  `IssueResult`, `PRCreatedEvent`, the registry) and the chat contract
  (`ChatCapable`, `ChatBridge`, `MessageEvent`, `OutboundMessage`).
- **Callbacks over imports.** Where a connector needs host behaviour (active-
  execution listing, sub-issue creation, message handling, metrics), it
  accepts an interface the host implements — it never imports the host.
- **Untrusted text is hostile input.** Anything from a third-party API (issue
  title, PR body, chat message) goes through `sdk/util/text` in the live
  poll/webhook/listener path *before* it can reach an LLM or downstream system.

## Install

```bash
go get github.com/qf-studio/studio-sdk@latest
```

Go 1.24+. The core and utility packages are stdlib-only.

## Layout

```
sdk/
  core/            # Adapter + chat contracts; registry; normalized events; priority
  log/             # Logger interface (a *slog.Logger satisfies it directly)
  testutil/        # Shared test helpers (fake tokens etc.)
  util/
    skipreason/    # Shared poller skip-reason constants + metrics interface
    text/          # Untrusted-text sanitization (anti prompt-injection)
  integrations/    # One package per connector
    asana/  azuredevops/  discord/  github/  gitlab/
    jira/   linear/       plane/    slack/   telegram/
```

## Connectors

| Connector     | Type          | Transport            | Status |
| ------------- | ------------- | -------------------- | ------ |
| `github`      | issue tracker | poll + webhook       | ✅     |
| `gitlab`      | issue tracker | poll + webhook       | ✅     |
| `azuredevops` | issue tracker | poll + webhook       | ✅     |
| `linear`      | issue tracker | poll + webhook       | ✅     |
| `jira`        | issue tracker | poll + webhook       | ✅     |
| `asana`       | issue tracker | poll + webhook       | ✅     |
| `plane`       | issue tracker | poll                 | ✅     |
| `telegram`    | chat          | long-poll            | ✅     |
| `slack`       | chat          | Socket Mode (WS)     | ✅     |
| `discord`     | chat          | Gateway (WS)         | ✅     |

## Runtime dependencies

The core and utility packages are stdlib-only. The only third-party runtime
dependency in the SDK is `github.com/gorilla/websocket v1.5.3`, pulled in by
`slack` (Socket Mode) and `discord` (Gateway). Every other connector is
stdlib-only at runtime.

## Issue-tracker usage

```go
import (
    "context"
    "os"

    "github.com/qf-studio/studio-sdk/sdk/core"
    "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"
)

// 1. Configure a connector. The trigger label/tag is host-neutral.
cfg := gitlab.DefaultConfig()
cfg.Token = os.Getenv("GITLAB_TOKEN")
cfg.Project = "namespace/project"
cfg.TriggerLabel = "pilot" // issues with this label are picked up

// 2. Wire host behaviour via callbacks (the SDK never imports the host).
deps := core.PollerDeps{
    Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
        // ev.Title / ev.Body are already sanitized; ev.Priority is normalized;
        // ev.SequenceID is provider-prefixed (e.g. "GL-42").
        return &core.IssueResult{Success: true}, nil
    }),
    OnPRCreated: func(ev core.PRCreatedEvent) { /* notify / track */ },
}

// 3. Run the poller.
poller := gitlab.New(cfg).NewPoller(deps)
_ = poller.Start(context.Background())
```

## Chat usage

```go
import (
    "context"
    "log/slog"
    "os"

    "github.com/qf-studio/studio-sdk/sdk/core"
    "github.com/qf-studio/studio-sdk/sdk/integrations/discord"
)

ad := discord.NewAdapter(discord.Config{
    BotToken:        os.Getenv("DISCORD_BOT_TOKEN"),
    AllowedGuildIDs: []string{"123"},
})

bridge := ad.NewChatBridge(core.ChatDeps{
    Logger: slog.Default(),
    HandleMessage: func(ctx context.Context, ev core.MessageEvent) error {
        // ev.Action is "message" | "command" | "callback".
        // ev.Text is already sanitized; ev.Command + ev.Args populated when Action=="command".
        // Bridge does NOT execute commands — the host router does.
        _, err := bridge.Send(ctx, core.OutboundMessage{
            ChannelID: ev.ChannelID,
            Text:      "ack: " + ev.Text,
        })
        return err
    },
})

_ = bridge.Start(context.Background())
```

## Contract reference

`sdk/core` is the one contract surface every connector conforms to.

**Issue-tracker contract**:
- Interfaces — `Adapter`, `Pollable`, `WebhookCapable`, `Poller`
- Events — `IssueEvent`, `IssueResult`, `PRCreatedEvent`
- Priority — `NormalizePriority(rank int) string` + the
  `PriorityNone/Urgent/High/Medium/Low` vocabulary
- Host callbacks — `IssueHandler`, `ProcessedStore`, `ActiveExecutionLister`

**Chat contract** (`sdk/core/chat.go`):
- Capability — `ChatCapable` (`NewChatBridge(ChatDeps) ChatBridge`)
- Lifecycle — `ChatBridge.Start(ctx)` / `Send` / `Edit` / `Ack`
- Events — `MessageEvent` (Action `"message"`/`"command"`/`"callback"`),
  `Identity`, `OutboundMessage` (Text + `Buttons []Button`), `MessageRef`
- Host callback — `ChatDeps.HandleMessage`. The bridge **normalizes**
  commands; it does NOT execute them.

Adding a connector: see `.agent/sops/integrations/authoring-a-connector.md`.
Architecture overview: `.agent/system/project-architecture.md`.
Chat-bridge design: `.agent/system/chat-bridge-design.md`.

## Roadmap

- **M7 — Pilot cutover** (next): wire Pilot to consume `sdk/integrations/*`
  instead of `internal/adapters/*`. Behavior-parity risks: `SequenceID` is now
  provider-prefixed; gitlab/azuredevops `Priority` is newly populated;
  gitlab/azuredevops `Body` is sanitized. Tracked at issue #27.
- **M8 — bot template** (later): higher-level scaffolding for "wire a Studio
  service to one issue tracker + one chat platform" in a single function call.
- **`v1.0.0`**: API stabilization once M7 has shaken out the contract.

## License

Internal — Studio projects only.
