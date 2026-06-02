# SOP: Tagging and Release

The SDK is a Go module consumed via `go get`; "release" = a git tag. No build
artifacts, no registry push. Current cadence: `v0.1.0` → `v0.8.0`, one minor
bump per landed milestone/connector.

## Versioning (while `v0.x`)

Pre-`v1.0.0`, the public API is unstable and breaking changes are allowed
(per CLAUDE.md). Convention used so far:

- **Minor** (`v0.N.0`) — a new connector lands, or a notable `sdk/core`
  contract change (e.g. the prompt-layer removal + `core.Priority` addition).
- **Patch** (`v0.N.M`) — fixes / docs / non-contract changes.

Call out every `sdk/core` contract break and every `IssueEvent` field-semantics
change (e.g. `SequenceID` now provider-prefixed, `Priority` now populated) in
the tag annotation and PR body — downstream hosts (Pilot at M7) depend on these.

## Release steps

```bash
# 1. main is green
git switch main && git pull
go build ./... && go vet ./... && go test -race ./... && gofmt -l sdk/

# 2. annotated tag (annotated, not lightweight — carries the changelog)
git tag -a v0.9.0 -m "v0.9.0: <summary>

- <contract/behavior changes>
- <new connectors>
- <breaking notes>"

# 3. push the tag
git push origin v0.9.0
```

Consumers then `go get github.com/qf-studio/studio-sdk@v0.9.0`.

## Notes

- Tag annotations are the de-facto changelog (there is no `CHANGELOG.md` yet;
  add one if release notes outgrow tag messages).
- `go.mod` currently has **no `require` block** — every ported connector so far
  is pure stdlib. The first connector needing an external client lib (e.g.
  GitHub's) will introduce `go.mod` requires + a `go.sum`; run `make tidy`
  (`go mod tidy`) and commit both before tagging.
- Never tag from a feature branch; tag the merge commit on `main`.
