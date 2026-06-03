# Studio SDK — Contributor & AI-Assistant Guide

## Context

Reusable Go connectors for issue trackers (GitHub, GitLab, Azure DevOps, Linear,
Jira, Asana, Plane) and chat platforms (Slack, Telegram, Discord). Each connector
normalizes a third-party API into the small contract surface in `sdk/core`, so a
host service can consume any of them the same way.

**Tech Stack**: Go 1.24 (stdlib-only core; per-connector client deps)
**Module**: `github.com/qf-studio/studio-sdk`
**Stability**: `v0.x` — public API unstable until `v1.0.0`

See `ARCHITECTURE.md` for the contract surface and `CONTRIBUTING.md` for how to
add a connector and run the gates.

---

## Load-Bearing SDK Constraints

These are not style preferences — they define what the SDK *is*:

1. **Zero host dependency.** `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil`
   depend only on the Go stdlib. Connector packages may pull their own client
   libs. Nothing in the SDK imports a consuming application.
2. **One contract surface.** Adapter contracts (`Adapter`, `Pollable`,
   `WebhookCapable`, `Poller`) and normalized events (`IssueEvent`,
   `IssueResult`, `PRCreatedEvent`), plus the chat contract (`ChatCapable`,
   `ChatBridge`, `MessageEvent`), live in `sdk/core`. Connectors conform; they
   do not extend.
3. **Callbacks over imports.** When a connector needs host behavior (active-
   execution listing, sub-issue creation, metrics), it accepts an interface the
   host implements. See `ActiveExecutionLister` for the canonical pattern.
4. **Untrusted text is hostile input.** Anything from a third-party API
   (issue title, PR body, chat message) goes through `sdk/util/text` before
   reaching an LLM or downstream system.

If a change can't satisfy these, it doesn't belong in the SDK.

---

## Go Code Standards

- **Stdlib first.** Reach for third-party only inside the specific connector.
- **Errors as values.** No panics in library code. Wrap with `%w`.
- **Small interfaces** at the consumer side (mostly in `sdk/core`).
- **`*slog.Logger` satisfies `sdk/log.Logger`** — don't hardcode `slog.Default()`.
- **`gofmt` + `go vet`** are mandatory. `golangci-lint` runs when installed.
- **`go test -race ./...`** is the CI standard.

---

## Forbidden Actions

- Importing a host application from `sdk/*`.
- Adding third-party deps to `sdk/core`, `sdk/log`, `sdk/util/*`, or `sdk/testutil`.
- Extending core contracts inside a connector instead of in `sdk/core`.
- Hardcoding loggers, secrets, or test tokens (use `sdk/log` + `sdk/testutil`).
- Committing secrets or `.env` files.
- Deleting tests without replacement.

---

## Build / Test / Lint

Driven by `Makefile`:

| Target       | Command                                            |
| ------------ | -------------------------------------------------- |
| `make build` | `go build ./...`                                    |
| `make test`  | `go test -race ./...`                               |
| `make lint`  | `go vet ./...` + `golangci-lint run` (if installed) |
| `make fmt`   | `gofmt -w .`                                        |
| `make tidy`  | `go mod tidy`                                       |

Run `make test` before opening a PR on any non-doc change.

---

## Commit Guidelines

- **Format**: `type(scope): description` (Conventional Commits).
- **Scope** matches the package touched (`core`, `plane`, `testutil`, ...).
- **Types**: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.
- Call out breaking `sdk/core` contract changes explicitly in the message and in
  the release notes — downstream hosts depend on them.
