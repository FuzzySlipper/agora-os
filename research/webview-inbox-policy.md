# Webview Inbox Write Policy

## Problem

`event-bus-web` originally allowed an agent token to publish to any `webview.inbox.<uid>.*` topic as an email-style model for browser-facing shell work.

That shape is easy to reason about, but it also means any agent with a valid token can flood another agent's inbox if those inboxes become load-bearing. Because the event bus is intentionally simple and local, a generic rate limiter here would add state and arbitrary thresholds without answering the more important product question: which cross-agent deliveries should be allowed at all?

## Decision

For agent tokens, `webview.inbox.<uid>.*` is owner-only by default:

- an agent may subscribe to its own inbox
- an agent may publish to its own inbox
- an agent may not publish directly to another agent's inbox through `event-bus-web`
- human-shell tokens keep full-feed access
- `webview.broadcast.*` remains the shared agent-publish namespace

The event bus sender-attribution model does not change. Published events are still stamped by the trusted local bus connection with the authenticated publishing uid rather than trusting any self-reported sender field.

## Rationale

This project generally prefers explicit, typed authority over ambient convenience. Default-open cross-agent inbox writes create a delivery permission that is broader than the current product model actually defines.

Choosing owner-only inbox writes now is safer than adding a best-effort rate limiter because:

- it removes the spam vector instead of trying to soften it
- it keeps the policy legible in code and tests
- it avoids inventing thresholds that would need revisiting once real shell workflows exist
- it leaves room for a future explicit router, allow-list, or supervisor-mediated delivery path if targeted cross-agent messaging becomes a real requirement

## Follow-on Shape

If Agora OS later needs agent-to-agent targeted delivery in the shell, that path should be explicit and reviewable:

- a root-owned or human-approved router publishes on behalf of the chosen workflow, or
- the policy grows an explicit allow-list contract rather than a blanket default-open rule

Until then, `webview.inbox.<uid>.*` should mean "messages destined for that uid and writable by that uid (or by the human shell)," not "an unrestricted peer-to-peer mailbox."
