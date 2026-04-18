# Event Bus Sender Identity

## Problem

The local event bus has always stamped `sender.uid` from kernel peer credentials for direct Unix-socket publishers. That keeps attribution authoritative for root-owned services like the compositor bridge, isolation service, and audit service.

`event-bus-web` complicates that story slightly: it is itself a trusted root-owned proxy, but it publishes on behalf of an authenticated web client. Before this task, a human-shell publish appeared on the bus as `sender.uid=0`, which was indistinguishable from a direct root-owned daemon publish.

## Decision

The bus now stamps an explicit `sender.kind` alongside `sender.uid`:

- `peer`: a direct Unix-socket publisher whose identity came from `SO_PEERCRED`
- `delegated`: a trusted root-owned proxy publish that preserves the authenticated subordinate uid

The important special case is:

- `sender.uid=0, sender.kind=peer` means a direct root-owned bus client
- `sender.uid=0, sender.kind=delegated` means a trusted proxy speaking for the human shell

## Why This Shape

Using a sentinel uid would overload the core identity primitive and make consumers learn a project-specific exception. Adding an explicit kind field keeps the uid truthful while exposing the one extra piece of context consumers actually need: whether the broker stamped the sender from a direct kernel peer or from a trusted delegation path.

This also scales better than a hard-coded "human" role inside the broker:

- the broker stays focused on attribution, not application roles
- future root-owned proxies can reuse the same delegated shape without inventing more sentinel ids
- consumers can still combine `sender.uid` with topic or payload semantics when they want finer display labels

## Interpretation

Consumers should treat `sender.uid` and `sender.kind` together as the authoritative sender metadata for bus events.

- For direct services on the Unix socket bus, trust `sender.kind=peer`
- For proxied web clients, trust `sender.kind=delegated`
- Treat payload identity fields as advisory routing/context only
