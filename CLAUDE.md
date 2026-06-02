# Studio SDK — Claude Code Configuration

## Context

Reusable Go connectors for Studio projects — issue trackers (GitHub, GitLab,
Azure DevOps, Linear, Jira, Asana, Plane) and chat platforms (Slack, Telegram,
Discord). Extracted from Pilot so any Studio Go service can consume the same
connectors without depending on Pilot itself.

**Tech Stack**: Go 1.24 (stdlib-only core; per-integration client deps)
**Module**: `github.com/qf-studio/studio-sdk`
**Stability**: `v0.x` — public API unstable until `v1.0.0`
**Navigator Version**: 6.15.5
**Last Updated**: 2026-06-02

---

## Navigator Quick Start

**Every session begins with**:
```
/nav:start
```

This loads `.agent/DEVELOPMENT-README.md` (the project navigator), the active
context marker, config, and any in-flight task docs.

**Core workflow**:
1. `/nav:start` → loads navigator
2. Load task docs → only what's needed for current work
3. Implement → follow the SDK constraints below
4. Document → `/nav:task archive <slug>` when a task is complete
5. `/nav:compact` after isolated sub-tasks

---

## Load-Bearing SDK Constraints

These are not style preferences — they define what the SDK *is*:

1. **Zero Pilot dependency.** `sdk/core`, `sdk/log`, `sdk/util/*`, `sdk/testutil`
   must depend only on the Go stdlib. Connector packages may pull their own
   client libs. Nothing in the SDK imports Pilot.
2. **One contract surface.** Adapter contracts (`Adapter`, `Pollable`,
   `WebhookCapable`, `Poller`) and normalized events (`IssueEvent`,
   `IssueResult`, `PRCreatedEvent`) live in `sdk/core`. Connectors conform; they
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

### SDK Constraint Violations (HIGHEST PRIORITY)
- Importing Pilot, or any host application, from `sdk/*`
- Adding third-party deps to `sdk/core`, `sdk/log`, `sdk/util/*`, or `sdk/testutil`
- Extending core contracts inside a connector instead of in `sdk/core`
- Hardcoding loggers, secrets, or test tokens (use `sdk/log` + `sdk/testutil`)

### Navigator Violations
- Loading the entire `.agent/` tree at once (defeats token optimization)
- Skipping `DEVELOPMENT-README.md` at session start
- Leaving task docs unarchived after completion

### General
- No Claude Code / Anthropic mentions in commits or code
- Never commit secrets or `.env` files
- Don't delete tests without replacement

---

## Documentation Structure

```
.agent/
├── DEVELOPMENT-README.md      # Navigator (always load first)
├── .nav-config.json           # Navigator config
├── tasks/                     # Implementation plans
├── system/                    # Architecture docs
│   ├── project-architecture.md
│   └── tech-stack-patterns.md
└── sops/                      # Standard Operating Procedures
    ├── integrations/
    ├── debugging/
    ├── development/
    └── deployment/
```

**Token-efficient loading**:
- Navigator: ~2k (always)
- Current task: ~3k (as needed)
- One system doc: ~5k (when relevant)
- One SOP: ~2k (if required)
- **Total**: ~12k vs ~150k loading everything

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

Run `make test` before requesting review on any non-doc change.

---

## Commit Guidelines

- **Format**: `type(scope): description`
  - Examples in history: `feat(core): ...`, `feat(plane): ...`,
    `chore(core): ...`, `feat(testutil): ...`
- **Scope** matches the package touched (`core`, `plane`, `testutil`, ...).
- **Types**: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.
- Call out breaking core contract changes explicitly in the message.
- No Claude Code mentions.

---

## Configuration

Navigator config in `.agent/.nav-config.json`.

---

**For complete Navigator documentation**:
- `.agent/DEVELOPMENT-README.md` — project navigator
- Plugin's root CLAUDE.md — full Navigator workflow reference
