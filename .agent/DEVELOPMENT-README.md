# Studio SDK - Development Documentation Navigator

**Project**: Reusable Go connectors for Studio projects (issue trackers + chat platforms), extracted from Pilot.
**Tech Stack**: Go 1.24 (stdlib-only core; per-integration client deps)
**Updated**: 2026-06-02

---

## Quick Start for Development

### New to This Project?
**Read in this order:**
1. [Project Architecture](./system/project-architecture.md) - Layout, contracts, integration model
2. [Tech Stack Patterns](./system/tech-stack-patterns.md) - Go conventions used here
3. README.md (repo root) - Public-facing overview

### Starting a New Feature?
1. Check if similar task exists in [`tasks/`](#implementation-plans-tasks)
2. Read relevant system docs from [`system/`](#system-architecture-system)
3. Check for integration SOPs in [`sops/`](#standard-operating-procedures-sops)
4. Create a task doc: `/nav:task new <slug>`

### Fixing a Bug?
1. Check [`sops/debugging/`](#debugging) for known issues
2. Review relevant system doc for context
3. After fixing, capture a SOP if non-obvious: `/nav:sop debugging <issue-name>`

---

## Documentation Structure

```
.agent/
├── DEVELOPMENT-README.md     ← You are here (navigator)
├── .nav-config.json          ← Navigator configuration
├── tasks/                    ← Implementation plans (one file per task)
├── system/                   ← Living architecture documentation
│   ├── project-architecture.md
│   └── tech-stack-patterns.md
├── sops/                     ← Standard Operating Procedures
│   ├── integrations/         #   Per-connector setup / quirks
│   ├── debugging/            #   Recurring issues + fixes
│   ├── development/          #   Build, test, lint workflows
│   └── deployment/           #   Tagging / release process
└── grafana/                  ← Optional Claude Code metrics dashboard
```

---

## SDK-Specific Constraints

These are load-bearing — every change should respect them:

- **Zero Pilot dependency.** `sdk/core` and `sdk/util/*` must depend only on the
  Go stdlib. Each `sdk/integrations/<name>` package may pull its own client deps.
- **One contract surface.** Adapter interfaces (`Adapter`, `Pollable`,
  `WebhookCapable`, `Poller`) and normalized event types (`IssueEvent`,
  `IssueResult`, `PRCreatedEvent`) live in `sdk/core`. Integrations conform; they
  do not extend.
- **Callbacks over imports.** When an integration needs host behavior (active-
  execution listing, sub-issue creation, metrics), it accepts an interface the
  host implements. Never import the host.
- **Public API is unstable (`v0.x`).** Breaking changes are allowed until
  `v1.0.0`, but every break should be noted in commits / release notes.

---

## When to Read What

### New connector (e.g., `sdk/integrations/<name>`)
1. `sops/development/driving-the-sdk-board.md` → how work reaches Pilot
   (board `Status=Todo` gate — the daemon ignores the label alone)
2. `sops/integrations/authoring-a-connector.md` → the recipe + contract reference
3. `system/project-architecture.md` → `sdk/core` contracts + boundary conventions
4. A full connector as structural reference (`gitlab` or `azuredevops`; `plane`
   is intentionally narrower — no merger/cleanup)
5. Implement, add tests (incl. an ASCII-smuggling sanitize guard)
6. Capture connector-specific quirks in `sops/integrations/<name>.md`

### Touching `sdk/core` contracts
1. Read **every** existing `sdk/integrations/*` package — they all conform
2. Check `tasks/` for in-flight migration work (e.g., SDK M2.x sequence)
3. Plan the change as a task doc before editing

### Debugging
1. `sops/debugging/` for known issues
2. Run `make test` or focused `go test ./sdk/...`
3. If novel → write a SOP

---

## Token Optimization Strategy

**Always load**: `DEVELOPMENT-README.md` (~2k tokens)
**Load for current work**: Specific task doc (~3k tokens)
**Load as needed**: One system doc (~5k tokens)
**Load if required**: One SOP (~2k tokens)

**Total**: ~12k tokens vs ~150k if loading everything.

---

## Commands Reference

- `/nav:start` — Load navigator + active marker + config
- `/nav:task new <slug>` — Create a task plan in `tasks/`
- `/nav:sop <category> <name>` — Capture a SOP
- `/nav:marker` — Save a context checkpoint (use before `/compact`)
- `/nav:compact` — Clear context while preserving knowledge

---

**Last Updated**: 2026-06-02
**Powered By**: Navigator 6.15.5
