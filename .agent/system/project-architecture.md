# Studio SDK — Project Architecture

**Updated**: 2026-06-02

## Purpose

Reusable Go connectors for Studio projects — issue trackers (GitHub, GitLab,
Azure DevOps, Linear, Jira, Asana, Plane) and chat platforms (Slack, Telegram,
Discord). The SDK is being extracted from Pilot so any Studio Go service can
consume the same connectors without taking on Pilot itself as a dependency.

## Module

- Module path: `github.com/qf-studio/studio-sdk`
- Go: `1.24.2`
- Stability: `v0.x` (unstable; breaking changes allowed until `v1.0.0`)

## Layout

```
sdk/
  core/            Adapter contracts, registry, normalized event types
  log/             Logger interface (a *slog.Logger satisfies it directly)
  testutil/        Shared test fakes (fake tokens, etc.)
  util/
    skipreason/    Shared poller skip-reason constants + metrics interface
    text/          Untrusted-text sanitization (prompt-injection defense)
  integrations/    One package per connector
    azuredevops/   (ported)
    gitlab/        (ported)
    plane/         (ported — narrower: no merger/cleanup, see SOP)
  doc.go           Package-level docs
```

Three connectors are ported (`plane`, `gitlab`, `azuredevops`). Remaining
issue trackers (`github`, `linear`, `jira`, `asana`) follow the same recipe;
the chat trio (`slack`, `telegram`, `discord`) is blocked on the core chat
bridge — see `system/chat-bridge-design.md`. To add a connector, follow
`sops/integrations/authoring-a-connector.md`.

## Contracts (sdk/core)

The core defines a single contract surface that every connector conforms to:

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

## Build / test / lint

Driven by `Makefile`:

- `make build` — `go build ./...`
- `make test`  — `go test -race ./...`
- `make lint`  — `go vet ./...` + `golangci-lint run` (if installed)
- `make fmt`   — `gofmt -w .`
- `make tidy`  — `go mod tidy`

## In-flight work

Migration from Pilot is sequenced. State lives in
`tasks/SDK-extraction-state.md`. Ported: `plane`, `gitlab`, `azuredevops`
(all conforming to the boundary conventions above). Next: the `github` /
`linear` / `jira` / `asana` issue-tracker quartet (mechanical recipe reuse),
then the chat trio once `system/chat-bridge-design.md` is implemented in
`sdk/core`.
