# Controlled X11/Xvfb automation fixture lane

This directory is reserved for future X11/Xvfb-only UI fixtures. It is a
compatibility lane for deterministic commodity automation, not an Agora desktop
architecture lane.

## Scope warning

These tests run under X11/Xvfb for deterministic commodity automation only.
They do not validate Agora production desktop semantics, Wayland isolation,
compositor bridge grants, input mediation, clipboard mediation, surface ownership,
or audit attribution. Any test that asserts those properties must run through the
real Wayland/VM path (`test/phase2.sh`, `test/phase3.sh`, or a future compositor
computer-use probe) instead.

## Appropriate uses

Use this lane only for fixtures where compositor identity and permission
semantics are irrelevant, such as:

- small browser or web UI component fixtures;
- Playwright/Selenium-style deterministic interaction tests;
- fixed-size screenshots or OCR artifacts for model evaluation;
- agent-sim comparison pages that intentionally measure commodity automation;
- fast iteration when the goal is to isolate UI/model behavior from Wayland
  session behavior.

## Not valid evidence for

Do not cite an X11/Xvfb pass as proof of any of these production properties:

- Wayland surface ownership or focus semantics;
- Agora compositor bridge viewport grants;
- `read_pixels`, `pointer`, `keyboard`, `semantic_tree`, or clipboard grant
  enforcement;
- denial of pre-grant capture/input;
- kernel uid attribution, `SO_PEERCRED`, cgroups, nftables, fanotify, or
  append-only audit behavior;
- Wayfire/wlroots protocol compatibility;
- human approval UX truthfulness.

If a test would be misleading because X11 grants ambient screenshot/input power,
it belongs in the real Wayland/VM lane instead.

## Future harness conventions

No harness scripts are added yet. When a concrete fixture exists, keep the lane
visibly separate:

- set `AGORA_X11_XVFB=1` and `AGORA_FIXTURE_KIND=x11-xvfb` in fixture logs;
- use an isolated disposable `DISPLAY=:<n>` owned by the fixture harness;
- place scripts under `test/fixtures/x11-xvfb/scripts/`;
- place static pages under `test/fixtures/x11-xvfb/pages/`;
- place scenario files under `test/fixtures/x11-xvfb/scenarios/`;
- keep runtime output under `test/fixtures/x11-xvfb/artifacts/` or `/tmp` and
  never commit generated artifacts;
- label CI/Den evidence explicitly as `fixture:x11-xvfb` / `not-wayland-proof`.

The authoritative production compositor tests remain the Wayfire/VM workflows in
`test/phase2.sh` and `test/phase3.sh`.
