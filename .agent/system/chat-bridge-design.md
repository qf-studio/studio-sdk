# Chat Bridge Design (APPROVED 2026-06-03)

**Status:** contract APPROVED — ready to land in `sdk/core` as an isolated PR,
then port `telegram` as the reference connector. Supersedes the original sketch
(which assumed `WebhookCapable` reuse + a single `Send(text)` — both wrong per
the adapter survey below).

## Problem

`sdk/core` is issue-tracker shaped (`Adapter`, `Pollable`, `WebhookCapable`,
`IssueEvent`, `IssueHandler`, `PollerDeps`). None models a conversation. The
chat trio needs a **parallel, equally-thin** contract — conforming, not
extending. Forcing chat through `IssueEvent` would overload fields with the
wrong semantics.

## What the adapter survey established (slack / telegram / discord)

- **Inbound is a long-running listener, NOT a webhook.** Slack = Socket Mode
  (WS); Telegram = long-poll (`getUpdates`); Discord = Gateway (WS, reconnect).
  → the contract needs a `Start(ctx)` loop (like `Poller`), not `WebhookCapable`.
- **Outbound drives a lifecycle:** confirmation → button press → progressive
  **edits** (same message id) → result. All three support **edit-by-id**
  (streaming) and **interactive buttons** with a **synchronous callback ack**.
- **Rich formatting is divergent** (Block Kit / Markdown+inline-keyboard /
  embeds+action-rows) → stays INSIDE adapters; never in `core`.
- **Sanitize** is done in Pilot's `comms.Handler` today → in the SDK it moves
  into the adapter live path (the SDK invariant: `MessageEvent.Text` is
  untrusted, sanitize before emission).
- The task-lifecycle UX (confirmation/progress/result composition, command
  execution, intent classification) is a **host** concern — the SDK only
  normalizes inbound messages and exposes send/edit/ack.

## Approved core additions (stdlib-only, in `sdk/core`)

```go
// --- inbound: one normalized event, Action-discriminated (like IssueEvent.Action) ---
type MessageEvent struct {
    Action     string   // "message" | "command" | "callback"
    MessageID  string   // adapter-native id (for replies/edits)
    ChannelID  string   // channel / chat / room
    ThreadID   string   // thread root; "" = top-level
    Text       string   // SANITIZED in the live path before emission
    Command    string   // "/run" when Action=="command"
    Args       []string // command args
    CallbackID string   // interactive id to Ack (Action=="callback")
    Data       string   // callback payload, e.g. "approve:TASK123"
    Sender     Identity
    Mentions   []string // user IDs
}
type Identity struct{ UserID, DisplayName string; IsBot bool }

// --- outbound: thin — Text + optional Buttons (APPROVED scope) ---
type OutboundMessage struct {
    ChannelID string
    ThreadID  string   // reply in-thread when set
    Text      string
    Buttons   []Button // optional; adapters render natively
}
type Button struct{ Label, ActionID, Data string }
type MessageRef struct{ ChannelID, MessageID, ThreadID string } // handle for edits

// --- contract surface (parallel to Pollable/Poller/PollerDeps) ---
type ChatCapable interface {            // ~ Pollable
    Adapter                             // Name() "slack" | "telegram" | "discord"
    NewChatBridge(ChatDeps) ChatBridge
}
type ChatBridge interface {             // ~ Poller
    Start(ctx context.Context) error
    Send(ctx context.Context, m OutboundMessage) (MessageRef, error)
    Edit(ctx context.Context, ref MessageRef, text string) error
    Ack(ctx context.Context, callbackID string) error
}
type MessageHandler interface {         // ~ IssueHandler
    HandleMessage(ctx context.Context, ev MessageEvent) error
}
type ChatDeps struct{ Handler MessageHandler } // ~ PollerDeps; callbacks-over-imports
```

## Resolved design decisions

1. **Outbound scope = Text + Buttons** (not text-only, not +attachments). All
   three platforms support buttons and Pilot's approve/cancel flow needs them;
   layout stays adapter-internal. Inbound attachments (Slack files, Telegram
   voice/photo) are **out of scope** for v1 — add later if a host needs them.
2. **Delivery = one `ChatBridge.Start(ctx)` listener** per adapter (long-poll /
   socket / gateway behind the same interface). Do NOT reuse `WebhookCapable`.
3. **Streaming = `Edit(ref, text)`** (all three support edit-by-id). No separate
   streaming abstraction.
4. **Callbacks = `MessageEvent{Action:"callback", CallbackID, Data}` + `Ack`.**
5. **Commands** — adapter sets `Action:"command"`, `Command`, `Args`; the host
   decides what to do (no command execution in `core`).
6. **Identity stays minimal** — opaque per-platform `UserID`; host resolves.

## Rollout (one phase at a time, reviewer-gated)

1. **Land the contract** above in `sdk/core` (`chat.go` + `chat_test.go`) as an
   ISOLATED PR — no connector. Compile-time conformance is proven by the first
   port. This is a `sdk/core` change → review every existing connector still
   builds; the new types must not perturb the issue contract.
2. **Port `telegram` first** (reference — simplest delivery: long-poll, plain
   REST, no WS gateway). Follow `sops/integrations/authoring-a-connector.md`;
   drop Pilot host-domain (intent/executor/memory/briefs/transcription), wire
   sanitize into the live listen path, conform to `ChatCapable`/`ChatBridge`.
3. **CHECKPOINT** — stop after telegram lands; review the contract against a
   real connector before porting `slack` (Block Kit + Socket Mode) and
   `discord` (Gateway). Those are not mechanical recipe reuse.
