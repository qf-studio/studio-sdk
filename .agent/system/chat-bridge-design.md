# Chat Bridge Design (proposed)

**Status:** design only — not yet implemented in `sdk/core`. This is the gate
that must be passed before porting `slack`, `telegram`, `discord`.

## Problem

`sdk/core` today is issue-tracker shaped: `Adapter`, `Pollable`,
`WebhookCapable`, `IssueEvent`, `IssueHandler`, `PollerDeps`. None of it models
a conversation. The chat trio needs: a message received, a reply sent, a
channel/thread, and a sender identity. Forcing chat through `IssueEvent` would
overload fields with the wrong semantics. We need a parallel, equally-thin
contract surface in `core` — conforming, not extending.

## Proposed core additions

Keep these stdlib-only and in `sdk/core`, mirroring the issue contract's shape.

```go
// MessageEvent is the normalized inbound chat message emitted by all chat adapters.
type MessageEvent struct {
    Action    string // "created", "edited"
    MessageID string // adapter-native message ID
    ChannelID string // channel / chat / room ID
    ThreadID  string // parent thread/root message; "" for top-level
    Text      string // SANITIZED before emission (see sanitize rule)
    Sender    Identity
    Mentions  []string // user IDs mentioned
}

type Identity struct {
    UserID      string
    DisplayName string
    IsBot       bool
}

// OutboundMessage is what a host asks an adapter to send.
type OutboundMessage struct {
    ChannelID string
    ThreadID  string // reply in-thread when set
    Text      string
}

// MessageHandler is the host's inbound entry point (mirrors IssueHandler).
type MessageHandler interface {
    HandleMessage(ctx context.Context, ev MessageEvent) (*OutboundMessage, error)
}

// ChatCapable is implemented by chat adapters (mirrors Pollable/WebhookCapable).
type ChatCapable interface {
    Adapter
    // Send delivers an outbound message (reply or new).
    Send(ctx context.Context, msg OutboundMessage) error
}

// ChatDeps is the chat analog of PollerDeps — host-supplied wiring.
type ChatDeps struct {
    Handler MessageHandler
    // optional: dedupe, metrics, etc. — add as needed, callbacks not imports
}
```

## Open design questions (resolve before coding)

1. **Streaming vs request/response.** Some platforms (Slack) support streaming
   edits to a message; others don't. Does `Send` return a handle for later
   edits, or do we add `Edit(ctx, MessageID, text)`? Recommend: start with
   `Send` + optional `Editable` sub-interface, don't over-model.
2. **Delivery model.** Slack/Discord are webhook + socket; Telegram is
   long-poll. Reuse `WebhookCapable` for the push platforms; add a
   `Listenable`/`Start(ctx)` analog (like `Poller`) for long-poll. Avoid a
   second polling abstraction if `Poller` can be generalized.
3. **Identity normalization.** Per-platform user IDs are opaque; the host maps
   them. Keep `Identity` minimal; don't try to resolve emails/handles in the SDK.
4. **Sanitize rule still applies.** `MessageEvent.Text` is untrusted — sanitize
   in the live path before emission, exactly as the issue connectors do.

## Why a separate surface (not reuse IssueEvent)

`IssueEvent` carries `SequenceID`, `Priority`, `Labels`, `ProjectID` — none map
to a chat message; `MessageEvent` carries `ThreadID`, `Sender`, `Mentions` —
none map to an issue. Two thin, honest contracts beat one overloaded struct.
The registry, `Adapter`, and the sanitize/logger/no-host-import rules are shared.

## Next step

Land `MessageEvent` / `MessageHandler` / `ChatCapable` / `ChatDeps` in
`sdk/core` (with tests) as an isolated PR, then port one chat connector
(recommend `telegram` — simplest delivery model) as the reference, following
`sops/integrations/authoring-a-connector.md`.
