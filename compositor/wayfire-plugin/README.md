# Wayfire Bridge Plugin

Thin in-process enforcement/data-extraction plugin for Phase 2.

This plugin owns only the compositor-local responsibilities that cannot safely
live on the Go side of the Unix-socket boundary:
- extract kernel-attested client credentials from `wl_client_get_credentials()`
- observe Wayfire view lifecycle/focus events
- forward typed surface events to the Go compositor bridge daemon
- maintain a local policy cache pushed down from the Go bridge
- deny keyboard/pointer delivery locally via cached policy lookups

It intentionally does **not** own higher-level grant or policy logic. The Go
bridge remains the policy authority; this plugin is the enforcement shim.

## Build

This subtree is an out-of-tree Meson project in the normal Wayfire style.

```sh
cd compositor/wayfire-plugin
meson setup build
meson compile -C build
```

Expected runtime dependencies:
- `wayfire`
- `wayland-server`
- `wf-config`

The current repo sandbox does not include the Wayfire SDK, so this subtree is
structured against upstream Wayfire headers but not compiled in-repo yet.

## Bridge Protocol

Transport: Unix socket, newline-delimited JSON.
Default socket path: `/run/agent-os/compositor-bridge.sock`

Plugin -> Go bridge event lines:

```json
{"type":"surface_event","event":"mapped","surface":{"id":"view-42","wayfire_view_id":42,"app_id":"org.example.App","title":"Example","role":"toplevel"},"client":{"pid":1234,"uid":60001,"gid":60001}}
{"type":"surface_event","event":"focused","surface":{"id":"view-42","wayfire_view_id":42,"app_id":"org.example.App","title":"Example","role":"toplevel"},"client":{"pid":1234,"uid":60001,"gid":60001}}
{"type":"surface_event","event":"input_denied","device":"keyboard","surface":{"id":"view-42","wayfire_view_id":42,"app_id":"org.example.App","title":"Example","role":"toplevel"},"client":{"pid":1234,"uid":60001,"gid":60001}}
```

Go bridge -> plugin policy updates:

```json
{"type":"policy_replace","surfaces":[{"surface_id":"view-42","owner_uid":60001,"allow_pointer_uids":[0,60001],"allow_keyboard_uids":[0,60001]}]}
{"type":"policy_upsert","surface":{"surface_id":"view-42","owner_uid":60001,"allow_pointer_uids":[0],"allow_keyboard_uids":[0]}}
{"type":"policy_remove","surface_id":"view-42"}
{"type":"input_context","actor_uid":60002}
```

Notes:
- `surface.id` is stable for the lifetime of the Wayfire view and is derived
  from `view->get_id()`.
- `actor_uid` identifies whose interaction context is currently being mediated.
  `0` is treated as the human/root context and bypasses cross-uid denial.
- Missing policy for a non-root actor is treated conservatively: no cross-uid
  input delivery.
