# Studio SDK

Reusable Go connectors for Studio projects — issue trackers (GitHub, GitLab,
Azure DevOps, Linear, Jira, Asana, Plane) and chat platforms (Slack, Telegram,
Discord), extracted from Pilot so any Studio Go service can consume them.

> **Status:** `v0.x` — unstable. The public API may change until `v1.0.0`.

## Design principles

- **Zero Pilot dependency.** The SDK depends only on the Go standard library
  and each integration's own client deps. Consuming applications wire their own
  services (scheduler, persistence, metrics, LLM runner) in via the interfaces
  and callbacks declared in `sdk/core`.
- **One contract surface.** `sdk/core` owns the adapter interfaces
  (`Adapter`, `Pollable`, `WebhookCapable`, `Poller`), the normalized event
  types (`IssueEvent`, `IssueResult`, `PRCreatedEvent`), and the registry.
- **Callbacks over imports.** Where an adapter needs host behaviour (active-
  execution listing, sub-issue creation, metrics), it accepts an interface the
  host implements — it never imports the host.

## Install

```bash
go get github.com/qf-studio/studio-sdk@latest
```

Go 1.24+. The core and utility packages are stdlib-only; each connector pulls
its own client deps.

## Layout

```
sdk/
  core/            # Adapter contract + registry + normalized events + priority
  log/             # Logger interface (a *slog.Logger satisfies it directly)
  util/
    skipreason/    # Shared poller skip-reason constants + metrics interface
    text/          # Untrusted-text sanitization (anti prompt-injection)
  integrations/    # One package per connector
    plane/  gitlab/  azuredevops/
```

## Connectors

| Connector | Status | Poll | Webhook |
| --- | --- | --- | --- |
| `plane` | ✅ | ✅ | ✅ |
| `gitlab` | ✅ | ✅ | ✅ |
| `azuredevops` | ✅ | ✅ | ✅ |
| `github`, `linear`, `jira`, `asana` | planned | — | — |
| `slack`, `telegram`, `discord` | planned (needs chat bridge) | — | — |

## Usage

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

## Contract reference

`sdk/core` is the one contract surface every connector conforms to:

- **Interfaces** — `Adapter`, `Pollable`, `WebhookCapable`, `Poller`
- **Events** — `IssueEvent`, `IssueResult`, `PRCreatedEvent`
- **Priority** — `NormalizePriority(rank int) string` + the
  `PriorityNone/Urgent/High/Medium/Low` vocabulary
- **Host callbacks** — `IssueHandler`, `ProcessedStore`, `ActiveExecutionLister`

Adding a connector? See `.agent/sops/integrations/authoring-a-connector.md`.
Architecture overview: `.agent/system/project-architecture.md`.

## Roadmap

Connectors land incrementally (extracted from Pilot). Issue-tracker quartet
(`github`/`linear`/`jira`/`asana`) is next; the chat trio is gated on the core
chat bridge (`.agent/system/chat-bridge-design.md`).

## License

TBD.
