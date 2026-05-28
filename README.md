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

## Layout

```
sdk/
  core/            # Adapter contract + registry + normalized event types
  log/             # Logger interface (a *slog.Logger satisfies it directly)
  util/
    skipreason/    # Shared poller skip-reason constants + metrics interface
    text/          # Untrusted-text sanitization (anti prompt-injection)
  integrations/    # One package per connector (added incrementally)
```

## Usage (sketch)

```go
import (
    "github.com/qf-studio/studio-sdk/sdk/core"
)

dep := core.PollerDeps{
    Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
        // host dispatches the issue to its executor
        return &core.IssueResult{Success: true}, nil
    }),
    OnPRCreated: func(ev core.PRCreatedEvent) {
        // host reacts to a new PR (autopilot, notifications, ...)
    },
}
_ = dep
```

## Roadmap

Connectors land incrementally. See the migration roadmap (extraction from
Pilot) for sequencing.

## License

TBD.
