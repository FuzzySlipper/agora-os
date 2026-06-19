#!/usr/bin/env python3
"""Drive the ASHA basic voxel interaction page through Agora compositorctl.

This is the Agora/environment half of Den task agora-os#2649. ASHA/asha-demo
owns voxel semantics and the public interaction page; this script launches that
page in Agora, drives the declared UI route with compositor input, captures
before/after surface evidence, and writes a proof sidecar that links the ASHA
artifact to Agora compositor captures.
"""
from __future__ import annotations

import argparse
import atexit
import json
import shlex
import sys
import time
from pathlib import Path
from typing import Any

# Reuse capture, PNG, command, cleanup, and port helpers from the #2631 proof.
SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))
import asha_agora_control_proof as common  # noqa: E402

SCENARIO_ID = "basic-voxel-landscape-interaction"
PAGE_TITLE = "ASHA Basic Voxel Interaction"
DEFAULT_ASHA_DEMO_ROOT = Path("/home/dev/asha-demo")
DEFAULT_COMPOSITORCTL = Path("/usr/local/bin/compositorctl")
DEFAULT_WEBVIEW_LAUNCHER = Path("/usr/local/bin/webview-launcher")
TASK_ID = 2649
PROJECT_ID = "agora-os"
AGENT_IDENTITY = "agora-runner"


class VoxelProofError(RuntimeError):
    def __init__(self, category: str, message: str, details: dict[str, Any] | None = None):
        super().__init__(f"{category}: {message}")
        self.category = category
        self.details = details or {}


def load_asha_artifact(root: Path) -> dict[str, Any]:
    artifact_path = root / "harness/out/voxel-interaction/latest/index.json"
    return json.loads(artifact_path.read_text())


def as_bool(value: Any) -> bool | None:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        if value.lower() == "true":
            return True
        if value.lower() == "false":
            return False
    return None


def read_page_markers(compositorctl: Path, surface_id: str, session_id: str, session_token: str, *, phase: str) -> dict[str, Any]:
    response = common.run_json([
        str(compositorctl), "--pretty", "app", "command",
        "--surface", surface_id,
        "--session", session_id,
        "--session-token", session_token,
        "--command", json.dumps({"type": "readiness", "scenarioId": SCENARIO_ID}),
        "--timeout-ms", "3000",
    ], timeout=20)
    result = response.get("result")
    if not isinstance(result, dict) or not result.get("ok"):
        raise VoxelProofError("surface_mapped_page_not_ready", "app-command readiness response was not ok", {"phase": phase, "response": response})
    markers = result.get("readiness")
    if not isinstance(markers, dict):
        raise VoxelProofError("surface_mapped_page_not_ready", "app-command readiness response did not include readiness markers", {"phase": phase, "response": response})
    markers = dict(markers)
    markers["request_id"] = response.get("request_id")
    markers["phaseObserved"] = phase
    return markers


def require_marker_state(markers: dict[str, Any], artifact: dict[str, Any], *, want_after: bool, phase: str) -> dict[str, Any]:
    failures: list[str] = []
    scenario = artifact.get("scenario", {}).get("id")
    selection_hash = artifact.get("selection", {}).get("selectionHash")
    render = artifact.get("render", {})
    mesh = artifact.get("mesh", {})

    if markers.get("title") != PAGE_TITLE:
        failures.append(f"title {markers.get('title')!r} != {PAGE_TITLE!r}")
    if markers.get("bodyReady") not in ("true", True):
        failures.append(f"bodyReady {markers.get('bodyReady')!r} != true")
    if markers.get("scenarioId") != scenario:
        failures.append(f"scenarioId {markers.get('scenarioId')!r} != {scenario!r}")
    if markers.get("selectionHash") != selection_hash:
        failures.append(f"selectionHash {markers.get('selectionHash')!r} != {selection_hash!r}")
    if markers.get("renderBeforeHash") != render.get("beforeHash"):
        failures.append("renderBeforeHash did not match ASHA artifact")
    if markers.get("renderAfterHash") != render.get("afterHash"):
        failures.append("renderAfterHash did not match ASHA artifact")
    if markers.get("meshBeforeHash") != mesh.get("beforeHash"):
        failures.append("meshBeforeHash did not match ASHA artifact")
    if markers.get("meshAfterHash") != mesh.get("afterHash"):
        failures.append("meshAfterHash did not match ASHA artifact")

    edit_applied = as_bool(markers.get("editApplied"))
    render_changed = as_bool(markers.get("renderChanged"))
    body_post_input = as_bool(markers.get("bodyPostInputRenderChanged"))
    mesh_changed = as_bool(markers.get("meshChanged"))
    expected_phase = "after-input-edit-applied" if want_after else "before-input-awaiting-edit"
    if edit_applied is not want_after:
        failures.append(f"editApplied {edit_applied!r} != {want_after!r}")
    if render_changed is not want_after:
        failures.append(f"renderChanged {render_changed!r} != {want_after!r}")
    if body_post_input is not want_after:
        failures.append(f"bodyPostInputRenderChanged {body_post_input!r} != {want_after!r}")
    if want_after and mesh_changed is not True:
        failures.append(f"meshChanged {mesh_changed!r} != True after input")
    if not want_after and markers.get("phase") != expected_phase:
        failures.append(f"phase {markers.get('phase')!r} != {expected_phase!r}")
    if want_after and markers.get("phase") != expected_phase:
        failures.append(f"phase {markers.get('phase')!r} != {expected_phase!r}")

    result = {
        "status": "ready" if not failures else "not_ready",
        "phase": phase,
        "expectedAfter": want_after,
        "markers": markers,
        "failures": failures,
    }
    if failures:
        raise VoxelProofError("surface_mapped_page_not_ready", "; ".join(failures), result)
    return result


def assert_accepted_input(key_result: dict[str, Any]) -> dict[str, int]:
    accepted = int(key_result.get("accepted") or 0)
    rejected = int(key_result.get("rejected") or 0)
    if accepted < 2 or rejected != 0:
        raise VoxelProofError("input_delivery_failed", f"expected accepted key press+release and zero rejects, got accepted={accepted} rejected={rejected}", {"keyResult": key_result})
    return {"accepted": accepted, "rejected": rejected}


def run_live(args: argparse.Namespace) -> int:
    asha_demo_root = Path(args.asha_demo_root).resolve()
    agora_repo = Path(args.agora_repo_root).resolve()
    compositorctl = Path(args.compositorctl)
    webview_launcher = Path(args.webview_launcher)
    if args.require_clean_agora_repo:
        common.require_clean_or_explain(agora_repo)
    if args.require_clean_asha_demo:
        common.require_clean_or_explain(asha_demo_root)

    common.run(["npm", "run", "voxel:interaction"], cwd=asha_demo_root, timeout=60)
    asha_artifact = load_asha_artifact(asha_demo_root)
    if asha_artifact.get("scenario", {}).get("id") != SCENARIO_ID:
        raise RuntimeError(f"ASHA artifact scenario mismatch: {asha_artifact.get('scenario')}")
    page_path = asha_demo_root / "harness/out/voxel-interaction/latest/index.html"
    if not page_path.exists():
        raise RuntimeError(f"generated page is missing: {page_path}")

    agora_commit = common.git_output(agora_repo, "rev-parse", "HEAD")
    agora_branch = common.git_output(agora_repo, "branch", "--show-current")
    audit_id = f"agora-os-{TASK_ID}-{int(time.time())}"

    session = common.run_json([
        str(compositorctl), "--pretty", "session", "create",
        "--label", SCENARIO_ID,
        "--project-id", PROJECT_ID,
        "--task-id", str(TASK_ID),
        "--agent-identity", AGENT_IDENTITY,
        "--asha-scenario", SCENARIO_ID,
        "--repo-commit", agora_commit,
        "--repo-branch", agora_branch,
        "--asha-runtime-mode", asha_artifact.get("runtime", {}).get("mode", ""),
        "--artifact-root", str(asha_demo_root / "harness/out/voxel-interaction/latest"),
        "--audit-correlation-id", audit_id,
    ])
    session_id = session["session_id"]
    session_token = session["session_token"]
    cleanup_state: dict[str, Any] = {"done": False, "result": None}

    def cleanup_session_once() -> dict[str, Any] | None:
        if args.keep_session_running:
            return None
        if cleanup_state["done"]:
            return cleanup_state["result"] if isinstance(cleanup_state["result"], dict) else None
        cleanup_state["done"] = True
        try:
            result = common.run([str(compositorctl), "--pretty", "session", "destroy", "--session", session_id], timeout=20)
            cleanup_state["result"] = {"status": "destroyed", "stdout": result.stdout.strip()}
        except Exception as exc:
            cleanup_state["result"] = {"status": "failed", "error": str(exc)}
        return cleanup_state["result"] if isinstance(cleanup_state["result"], dict) else None

    atexit.register(cleanup_session_once)

    app_command_port = common.free_loopback_port()
    launch_cmd = " ".join([
        shlex.quote(str(webview_launcher)),
        "--path", shlex.quote(str(page_path)),
        "--title", shlex.quote(PAGE_TITLE),
        "--app-id", shlex.quote("org.agora.ASHAVoxelInteraction"),
        "--width", str(args.width),
        "--height", str(args.height),
        "--app-command-port", str(app_command_port),
    ])
    launch = common.run_json([
        str(compositorctl), "--pretty", "launch",
        "--session", session_id,
        "--session-token", session_token,
        "--cwd", str(asha_demo_root),
        "--cmd", launch_cmd,
        "--expected-title", PAGE_TITLE,
        "--wait-surface",
        "--wait-timeout-ms", str(args.wait_timeout_ms),
    ], timeout=max(30, args.wait_timeout_ms // 1000 + 5))
    surface = launch.get("surface") or {}
    surface_id = surface.get("surface", {}).get("id") or surface.get("id")
    if not surface_id:
        raise VoxelProofError("no_surface", f"launch response did not include surface id: {launch}")

    readiness_notes: list[str] = []
    try:
        common.run_json([str(compositorctl), "--pretty", "wait", "for-render-idle", "--surface", surface_id, "--idle-ms", "500", "--timeout", str(args.wait_timeout_ms)], timeout=20)
    except RuntimeError as exc:
        readiness_notes.append(f"initial render-idle wait timed out; continuing because surface is mapped/capturable: {exc}")
    common.maybe_sleep(args.settle_seconds)

    before = common.capture(compositorctl, session_id, session_token, surface_id, "before-voxel-edit")
    before_path = common.capture_image_path(before)
    before_inspection = common.inspect_png(before_path)
    before_markers = read_page_markers(compositorctl, surface_id, session_id, session_token, phase="before-input")
    before_readiness = require_marker_state(before_markers, asha_artifact, want_after=False, phase="before-input")
    if before_inspection.get("status") != "visible":
        raise VoxelProofError("capture_visible_proof_content_missing", "before capture is blank/transparent", {"inspection": before_inspection, "readiness": before_readiness})

    before_frame = surface.get("frame_count", 0) if isinstance(surface, dict) else 0
    key_result = common.run_json([
        str(compositorctl), "--pretty", "key",
        "--session", session_id,
        "--session-token", session_token,
        "--surface", surface_id,
        "--key", args.input_key,
    ], timeout=20)
    input_delivery = assert_accepted_input(key_result)
    try:
        wait_frame = common.run_json([str(compositorctl), "--pretty", "wait", "for-frame", "--surface", surface_id, "--after-frame", str(before_frame), "--timeout", "3000"], timeout=10)
    except RuntimeError as exc:
        wait_frame = {"ok": False, "note": f"frame wait timed out after input: {exc}"}
    try:
        common.run_json([str(compositorctl), "--pretty", "wait", "for-render-idle", "--surface", surface_id, "--idle-ms", "250", "--timeout", "5000"], timeout=10)
    except RuntimeError as exc:
        readiness_notes.append(f"post-input render-idle wait timed out; capture still attempted: {exc}")
    common.maybe_sleep(args.settle_seconds)

    after = common.capture(compositorctl, session_id, session_token, surface_id, "after-voxel-edit")
    after_path = common.capture_image_path(after)
    after_inspection = common.inspect_png(after_path)
    after_markers = read_page_markers(compositorctl, surface_id, session_id, session_token, phase="after-input")
    after_readiness = require_marker_state(after_markers, asha_artifact, want_after=True, phase="after-input")
    if after_inspection.get("status") != "visible":
        raise VoxelProofError("capture_visible_proof_content_missing", "after capture is blank/transparent", {"inspection": after_inspection, "readiness": after_readiness})

    visual_change = common.compare_pngs(before_path, after_path)
    if visual_change.get("status") != "changed":
        comparison = {"status": "mismatched", "reason": "agent-input-produced-no-pixel-change"}
    else:
        comparison = {"status": "comparable", "reason": "before-after-visible-change-after-public-page-input"}

    cleanup = cleanup_session_once()
    proof = {
        "schemaVersion": 1,
        "scenarioId": SCENARIO_ID,
        "projectId": PROJECT_ID,
        "taskId": TASK_ID,
        "generatedAt": int(time.time()),
        "agora": {
            "repo": str(agora_repo),
            "branch": agora_branch,
            "commit": agora_commit,
            "sessionId": session_id,
            "launchId": launch.get("launch_id"),
            "surfaceId": surface_id,
            "auditCorrelationId": audit_id,
            "appCommandPort": app_command_port,
        },
        "ashaDemo": {
            "repo": str(asha_demo_root),
            "branch": asha_artifact.get("repo", {}).get("branch"),
            "commit": asha_artifact.get("repo", {}).get("commit"),
            "artifact": str(asha_demo_root / "harness/out/voxel-interaction/latest/index.json"),
            "page": str(page_path),
            "artifactLinkStatus": "local-path",
        },
        "ashaEvidence": {
            "scenario": asha_artifact.get("scenario"),
            "runtime": asha_artifact.get("runtime"),
            "selection": asha_artifact.get("selection"),
            "edit": asha_artifact.get("edit"),
            "mesh": asha_artifact.get("mesh"),
            "render": asha_artifact.get("render"),
            "controlSurface": asha_artifact.get("controlSurface"),
            "renderEvidencePolicy": asha_artifact.get("renderEvidencePolicy"),
        },
        "control": {
            "route": "Agora compositor keyboard injection to ASHA page declared Enter key route",
            "declaredInputs": asha_artifact.get("controlSurface", {}).get("declaredInputs"),
            "inputKey": args.input_key,
            "delivery": input_delivery,
            "keyResult": key_result,
            "waitFrame": wait_frame,
        },
        "readiness": {
            "policy": "surface must map; before capture must expose ASHA scenario/selection/render markers with editApplied=false; after compositor input must expose editApplied=true, postInputRenderChanged=true, mesh/render hash markers matching the ASHA artifact; captures must be visible/nonblank and before/after pixels must differ",
            "backend": "webview-app-command",
            "before": before_readiness,
            "after": after_readiness,
            "notes": readiness_notes,
        },
        "captures": {
            "before": before,
            "after": after,
            "beforeInspection": before_inspection,
            "afterInspection": after_inspection,
            "visualChange": visual_change,
        },
        "comparison": comparison,
        "cleanup": {"sessionDestroyedAfterCapture": not args.keep_session_running, "result": cleanup},
    }
    out_path = asha_demo_root / "harness/out/voxel-interaction/latest/agora-voxel-interaction-proof.json"
    out_path.write_text(json.dumps(proof, indent=2) + "\n")
    summary = {
        "proof": str(out_path),
        "comparison": comparison,
        "session_id": session_id,
        "launch_id": launch.get("launch_id"),
        "surface_id": surface_id,
        "before_capture": before.get("artifact", {}).get("artifact_id") or before.get("request_id"),
        "after_capture": after.get("artifact", {}).get("artifact_id") or after.get("request_id"),
        "before_image": str(before_path),
        "after_image": str(after_path),
        "changed_pixel_ratio": visual_change.get("changedPixelRatio"),
        "input_delivery": input_delivery,
    }
    print(json.dumps(summary, indent=2))
    return 0 if comparison["status"] == "comparable" else 2


def self_test() -> int:
    artifact = {
        "scenario": {"id": SCENARIO_ID},
        "selection": {"selectionHash": "fnv1a64:selection"},
        "render": {"beforeHash": "sha256:before", "afterHash": "sha256:after"},
        "mesh": {"beforeHash": "sha256:mesh-before", "afterHash": "sha256:mesh-after"},
    }
    base_markers = {
        "title": PAGE_TITLE,
        "bodyReady": "true",
        "scenarioId": SCENARIO_ID,
        "selectionHash": "fnv1a64:selection",
        "renderBeforeHash": "sha256:before",
        "renderAfterHash": "sha256:after",
        "meshBeforeHash": "sha256:mesh-before",
        "meshAfterHash": "sha256:mesh-after",
    }
    before = dict(base_markers, editApplied=False, renderChanged=False, bodyPostInputRenderChanged="false", phase="before-input-awaiting-edit")
    after = dict(base_markers, editApplied=True, renderChanged=True, bodyPostInputRenderChanged="true", meshChanged=True, phase="after-input-edit-applied")
    assert require_marker_state(before, artifact, want_after=False, phase="before")["status"] == "ready"
    assert require_marker_state(after, artifact, want_after=True, phase="after")["status"] == "ready"
    try:
        require_marker_state(dict(before, editApplied=True), artifact, want_after=False, phase="bad-before")
    except VoxelProofError as exc:
        assert exc.category == "surface_mapped_page_not_ready", exc
    else:
        raise AssertionError("expected marker state failure")
    assert_accepted_input({"accepted": 2, "rejected": 0})
    try:
        assert_accepted_input({"accepted": 1, "rejected": 0})
    except VoxelProofError as exc:
        assert exc.category == "input_delivery_failed", exc
    else:
        raise AssertionError("expected input delivery failure")
    print("self-test passed")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--asha-demo-root", default=str(DEFAULT_ASHA_DEMO_ROOT))
    parser.add_argument("--agora-repo-root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--compositorctl", default=str(DEFAULT_COMPOSITORCTL))
    parser.add_argument("--webview-launcher", default=str(DEFAULT_WEBVIEW_LAUNCHER))
    parser.add_argument("--width", type=int, default=980)
    parser.add_argument("--height", type=int, default=720)
    parser.add_argument("--wait-timeout-ms", type=int, default=15000)
    parser.add_argument("--settle-seconds", type=float, default=0.25)
    parser.add_argument("--input-key", default="enter", choices=("enter", "return", "space"))
    parser.add_argument("--keep-session-running", action="store_true", help="leave the launched webview/session running after capture")
    parser.add_argument("--require-clean-agora-repo", action="store_true", help="refuse to run if agora-os has local changes")
    parser.add_argument("--require-clean-asha-demo", action="store_true", help="refuse to run if asha-demo has local changes")
    parser.add_argument("--self-test", action="store_true", help="run script unit self-test without compositor access")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if args.self_test:
        return self_test()
    return run_live(args)


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except Exception as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
