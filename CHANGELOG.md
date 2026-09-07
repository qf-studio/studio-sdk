# Changelog

All notable changes to this module are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this
project is `v0.x`, so breaking changes may still land in minor releases.

## [Unreleased]

### Fixed

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
