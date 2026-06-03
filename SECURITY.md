# Security Policy

## Supported versions

The SDK is pre-`v1.0.0`; security fixes are applied to the latest minor release.
Pin a released tag (`go get github.com/qf-studio/studio-sdk@vX.Y.Z`) and upgrade
forward to receive fixes.

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Report privately via GitHub's **"Report a vulnerability"** flow:
**Security → Advisories → Report a vulnerability** on the repository. This opens
a private advisory visible only to the maintainers.

When reporting, include:

- the affected connector or package (e.g. `sdk/integrations/slack`, `sdk/util/text`),
- the version or commit,
- a description and, ideally, a minimal reproduction.

We aim to acknowledge a report within a few business days and to coordinate a
fix and disclosure timeline with you.

## Scope notes

- **Untrusted third-party text** (issue titles, PR bodies, chat messages) is
  treated as hostile input and is sanitized via `sdk/util/text` in the live
  poll / webhook / listener path before it reaches a host or an LLM. A bypass of
  that sanitization in any connector's live path is in scope — please report it.
- Each connector authenticates with the credentials the host supplies; the SDK
  never stores or logs them. Do not paste real tokens into issues or reproductions.
