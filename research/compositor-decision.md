# Compositor Decision: Pinnacle vs Wayfire

## Verdict

**Wayfire** for Phase 2, with a thin C++ bridge plugin (~300-500 lines).  
**Smithay-based custom compositor** remains the Phase 4 long-horizon endpoint.  
**Pinnacle's gRPC API is not viable** for agent-native surface mediation in its current form.

---

## The load-bearing question

> Can agent coordination logic live entirely out-of-process behind the gRPC boundary?

**No.** Pinnacle's gRPC API lacks the primitives needed for agent-native surface mediation:

1. **No client PID/UID exposure.** Windows are identified by opaque `window_id` (uint32). You can query `app_id` (self-reported Wayland app ID string) and `title`, but not the PID or Linux UID of the process that created the surface. Without kernel-attested identity at the surface level, there is no way to attribute surfaces to agent UIDs — the core primitive of the entire coordination model.

2. **No input event filtering.** The input API (`input.proto`) handles keybind/mousebind registration — compositor configuration, not real-time event mediation. There is no mechanism to say "deny input to surface X" or "intercept focus change to surface owned by UID Y."

3. **No per-event policy enforcement.** The gRPC boundary introduces a round-trip for every policy decision. Focus changes and input routing require sub-millisecond decisions; routing these through gRPC to an out-of-process Go service adds measurable latency on every keystroke and pointer event.

Pinnacle internally uses Smithay, which provides `Client::get_credentials()` (kernel-attested PID/UID via `SO_PEERCRED` on the Wayland socket). These capabilities exist inside the compositor but are **not exposed through the gRPC API**. The API is designed for window manager configuration scripting (like AwesomeWM's Lua), not for security-critical access control.

---

## Spike question answers

### 1. Can we observe surface create/destroy/focus with PID attribution?

**Pinnacle:** Partially. `signal.proto` provides streaming signals for `WindowCreated`, `WindowDestroyed`, `WindowFocused`, `WindowPointerEnter/Leave`, and `WindowTitleChanged`. These deliver `window_id` but **not PID or UID**. The `window.proto` has `GetAppId` (self-reported string) but no `GetPid` or `GetUid`. There is no way to resolve a `window_id` to a Linux UID through the gRPC API.

**Wayfire:** Yes. The plugin signal system provides `view-mapped`, `view-unmapped`, and `view-focused` events. From any view, you reach the underlying `wlr_surface`, then `wl_resource_get_client()`, then `wl_client_get_credentials(&pid, &uid, &gid)`. This is the same `SO_PEERCRED` mechanism used by the isolation service — kernel-attested, unspoofable.

### 2. Can we deny/intercept input events by surface UID?

**Pinnacle:** No. The gRPC API provides keybind registration, not input event interception. There is no mechanism to filter, deny, or conditionally route input events based on the target surface or its owning UID.

**Wayfire:** Yes, with a custom plugin. Plugins can install pointer and keyboard grabs (`wf::pointer_interaction_t`, `wf::keyboard_interaction_t`) that intercept events before they reach the focused surface. A bridge plugin can check the target surface's UID against a local policy cache and consume (drop) events that violate the policy. The policy cache is populated by the Go service over a Unix socket — the per-event check is a local map lookup, no IPC.

### 3. Is there a stable extension surface?

**Pinnacle:** The gRPC API is defined via `.proto` files under `api/protobuf/pinnacle/`. It is versioned (`v1`) and language-agnostic. However, the README notes the project is under "heavy development with breaking changes and complete rewrites of the APIs expected." The project has 576 stars and a single primary maintainer. Last release: v0.2.3 (February 2026). Active commits through April 2026.

**Wayfire:** Plugins compile against C++ headers under `wayfire/`. The top-level plugin interface (`wf::plugin_interface_t`) has been fairly stable, but plugins couple to wlroots types (e.g., `wlr_surface`). Each wlroots minor version (roughly annual) introduces breaking changes that require plugin porting. Wayfire 0.10 (2025) tracks wlroots 0.19. The project is actively maintained by a single primary developer (Ilia Bozhinov) with ~2.5k stars.

### 4. Can coordination logic live out-of-process?

**With Pinnacle:** No. The gRPC API is missing the security primitives (PID/UID, input interception, policy enforcement). Extending the API upstream is theoretically possible but would require maintaining a fork of a rapidly-evolving compositor — significant ongoing cost.

**With Wayfire:** Yes, with the right split. The architecture:
- **In-process (C++ plugin, ~300-500 lines):** Credential extraction, signal forwarding, policy cache, input grab enforcement. This is the thin enforcement layer.
- **Out-of-process (Go service):** Policy decisions, audit logging, integration with isolation service. Pushes policy updates to the plugin over a Unix socket.

The plugin is the enforcement point; the Go service is the policy authority. Per-event decisions are local lookups; policy changes are pushed asynchronously. This matches the "wire protocol boundary" design invariant from AGENTS.md — the Go services and the C++ compositor communicate over a Unix socket, not FFI.

---

## Architecture comparison

```
Pinnacle model (not viable):
  Compositor ──gRPC──▶ Go service ──decision──▶ gRPC ──▶ Compositor
  (every input event requires round-trip; PID/UID not available)

Wayfire model (recommended):
  Go service ──socket──▶ policy cache in C++ plugin
  Compositor ──local lookup──▶ allow/deny
  (per-event check is O(1) map lookup; policy updates are async)
```

---

## Risk assessment

| Risk | Mitigation |
|------|------------|
| Wayfire single-maintainer | Bridge plugin pattern is portable to other wlroots compositors (labwc, etc.) with moderate effort. The C++ surface is intentionally small. |
| wlroots API churn | Plugin touches `wl_client_get_credentials` (stable libwayland, will not change) and `wf::view_interface_t` signals (semi-stable). Expect annual porting effort proportional to plugin size (~300-500 lines). |
| C++ in the development loop | Confined to the bridge plugin only. All policy logic, audit integration, and service orchestration remain in Go. The plugin is write-once-per-wlroots-version, not actively developed. |
| Clipboard/screencopy mediation | Harder than input mediation — requires deeper wlroots hooks. Defer to Phase 3/4. For Phase 2, focus on surface attribution and input deny. |
| Pinnacle improves its API | Monitor upstream. If PID/UID and input interception land in the gRPC API, re-evaluate. But these are architectural additions, not incremental features — unlikely without our use case driving them. |

---

## Recommendation

1. **Phase 2:** Wayfire + thin C++ bridge plugin. The plugin extracts client credentials, forwards surface lifecycle events to a Go compositor-bridge service over a Unix socket, and enforces input policy from a local cache. All coordination logic stays in Go.

2. **Phase 4:** Smithay-based custom compositor if the Wayfire bridge proves too limiting (clipboard mediation, screencopy control, wpe-webkit surface ownership). Smithay provides `Client::get_credentials()` natively and is backed by System76/COSMIC, giving it a stronger sustainability story than wlroots long-term.

3. **Not Pinnacle** for Phase 2. The gRPC API is the right idea (language-agnostic, out-of-process control) but the current surface area is designed for window manager scripting, not security mediation. The missing primitives (PID/UID, input interception) are not gaps that can be papered over — they would require extending the API upstream or maintaining a fork.

---

## Evidence sources

- Pinnacle proto files: `api/protobuf/pinnacle/{window,signal,input}/v1/*.proto` (GitHub, pinnacle-comp/pinnacle, commit 88889905, April 2026)
- Pinnacle Rust API docs: pinnacle-comp.github.io/rust-reference/main/pinnacle_api/
- Wayfire plugin API: `wayfire/{view,plugin,core,signal-definitions}.hpp` headers
- `wl_client_get_credentials()`: libwayland-server, man page wl_client(3)
- Smithay `Client::get_credentials()`: smithay/wayland-server crate
- Wayland security model: blog.ce9e.org/posts/2025-10-03-wayland-security/
