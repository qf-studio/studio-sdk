# Studio SDK — Go Patterns Used Here

**Updated**: 2026-06-02

These are the conventions a contributor should follow when touching this repo.

## Imports & dependencies

- **Stdlib first.** Core, log, util, and testutil packages must import stdlib
  only. Reach for a third-party dep only inside the specific integration that
  needs it.
- **No host imports.** If a connector needs host behavior, accept an interface
  in core (see `ActiveExecutionLister`) and let the host satisfy it.

## Errors

- Errors are values. Return them; don't panic in library code.
- Wrap with `fmt.Errorf("...: %w", err)` so callers can `errors.Is` / `errors.As`.

## Interfaces

- Interfaces live next to the consumer, not the implementor (Go idiom). Most
  contracts live in `sdk/core` because the host (consumer) needs them.
- Keep them small. `Adapter`, `Pollable`, `WebhookCapable`, `Poller` are
  intentionally narrow — a connector implements only the capabilities it has.

## Logging

- `sdk/log` defines a minimal `Logger` interface. `*slog.Logger` satisfies it
  directly, so callers can pass `slog` without an adapter.
- Library code should accept a `Logger` (or accept nil and no-op); never
  hardcode `log.Println` or `slog.Default()`.

## Untrusted text

- Any text that originates from a third-party API (issue title, PR body, chat
  message) is untrusted. Run it through `sdk/util/text` before it touches an
  LLM or a downstream system. This is anti-prompt-injection plumbing, not
  cosmetic sanitization.

## Polling

- Shared skip reasons live in `sdk/util/skipreason` so every poller emits
  comparable metrics. New skip cases belong there, not in a connector-local
  constant.

## Testing

- `go test -race ./...` is the CI standard — write tests safe under `-race`.
- Use `sdk/testutil` for shared fakes (e.g. fake token constants). Don't
  invent ad-hoc test secrets in connector tests.
- Each connector should have unit tests; integration tests against the real
  third-party API are out of scope for the SDK and stay in the host.

## Versioning

- Public API is `v0.x` until contracts are stable. Breaking changes are OK
  but should be called out in commit messages and release notes.
- Use conventional commit prefixes already in use: `feat(scope):`,
  `fix(scope):`, `chore(scope):`, `refactor(scope):`, `test(scope):`.
