# Changelog

All notable changes to this module are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this
project is `v0.x`, so breaking changes may still land in minor releases.

## [Unreleased]

### Fixed

- **github/poller**: the `hasCompletedExecution` skip log line no longer
  always claims "completed execution exists". `sdk/core` gained
  `ExecutionCheckerV2`, an optional evolution of `ExecutionChecker` whose
  `HasCompletedExecutionReason` returns a host-supplied reason alongside the
  skip decision; the poller logs that reason verbatim (e.g.
  `reason="repick-backoff cooldown"`) and only falls back to the generic
  "completed execution exists" message when the host implements the old
  interface, or the new one with an empty reason. Deferred from pilot PR
  #5393 (pilot issue #5381 item 4). (GH-142)

  **Consumers whose `ExecutionChecker` skips re-dispatch for reasons other
  than a genuinely completed execution** (e.g. a repick-backoff cooldown)
  should implement `ExecutionCheckerV2` and bump their `studio-sdk` pin to
  get an accurate skip log line instead of maintaining their own
  workaround log line beforehand.

- **github/poller**: `prReferencesIssue` no longer treats a bare `#<n>`
  reference anywhere in a PR body as delivery of issue `n`. Previously, the
  autopilot CI-fix PR template's `- **Original Issue**: #275` metadata line
  (and incidental mentions like `Reverts PR #275`, `Refs #275`, `Follow-up to
  #275`) caused `hasMergedWork`'s branch-lookup fallback to mark issue #275
  done from an unrelated merged PR (#278) that actually delivered a
  different issue (#277). Only a `GH-<n>:` title prefix, or a closing
  keyword (`close(s/d)`, `fix(es/ed)`, `resolve(s/d)`) immediately followed
  by `#<n>`, `GH-<n>`, or a `.../issues/<n>` URL, now counts as a delivery
  claim. Follow-up to the GH-138 branch-reuse guard (#139). (GH-140)

  **Consumers pinning `studio-sdk` must bump to pick up this fix** — hosts
  polling GitHub issues via `sdk/integrations/github` may otherwise
  self-seal issues as done based on a bare `#n` mention in an unrelated
  merged PR's body.
