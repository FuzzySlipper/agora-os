#!/usr/bin/env python3
"""Drive the ASHA first-person Agora-control page through compositorctl.

This is the Agora/environment half of Den task agora-os#2631.  The script keeps
ASHA semantics in asha-demo: it first asks asha-demo to generate its public
camera-control artifact/page, then uses Agora's compositor session, app launch,
keyboard injection, capture, and artifact export path to prove before/after
visual change.
"""
from __future__ import annotations

import argparse
import atexit
import hashlib
import json
import os
import re
import shlex
import socket
import struct
import subprocess
import sys
import time
import zlib
from dataclasses import dataclass
from pathlib import Path
from typing import Any

SCENARIO_ID = "first-person-agora-control-basic"
DEFAULT_ASHA_DEMO_ROOT = Path("/home/dev/asha-demo")
DEFAULT_COMPOSITORCTL = Path("/usr/local/bin/compositorctl")
DEFAULT_WEBVIEW_LAUNCHER = Path("/usr/local/bin/webview-launcher")
TASK_ID = 2631
PROJECT_ID = "agora-os"
AGENT_IDENTITY = "agora-runner"


@dataclass(frozen=True)
class RunResult:
    argv: list[str]
    stdout: str
    stderr: str


@dataclass(frozen=True)
class PageReadinessExpectation:
    scenario_id: str
    step: int
    projection_hash: str | None = None
    phase: str = "initial"


class ProofReadinessError(RuntimeError):
    def __init__(self, category: str, message: str, details: dict[str, Any] | None = None):
        super().__init__(f"{category}: {message}")
        self.category = category
        self.details = details or {}


def run(argv: list[str], *, cwd: Path | None = None, timeout: int = 30) -> RunResult:
    proc = subprocess.run(
        argv,
        cwd=str(cwd) if cwd else None,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"command failed ({proc.returncode}): {shlex.join(argv)}\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
        )
    return RunResult(argv=argv, stdout=proc.stdout, stderr=proc.stderr)


def run_json(argv: list[str], *, cwd: Path | None = None, timeout: int = 30) -> dict[str, Any]:
    result = run(argv, cwd=cwd, timeout=timeout)
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"expected JSON from {shlex.join(argv)}: {exc}\n{result.stdout}") from exc
    if not isinstance(value, dict):
        raise RuntimeError(f"expected JSON object from {shlex.join(argv)}, got {type(value).__name__}")
    return value


def git_output(repo: Path, *args: str) -> str:
    return run(["git", *args], cwd=repo).stdout.strip()


def require_clean_or_explain(repo: Path) -> None:
    status = git_output(repo, "status", "--short")
    if status:
        raise RuntimeError(f"{repo} has local changes; refusing to mix live proof with a dirty checkout:\n{status}")


def load_asha_artifact(root: Path) -> dict[str, Any]:
    artifact_path = root / "harness/out/camera-agora-control/latest/index.json"
    return json.loads(artifact_path.read_text())


def command_sequence(artifact: dict[str, Any]) -> list[dict[str, str]]:
    commands = artifact["controlSurface"]["domRoute"]["keyboard"]
    return [
        {"command": "moveForward", "key": "w", "code": "KeyW", "sequence_id": "move-forward"},
        {"command": "lookRight", "key": "right", "code": "ArrowRight", "sequence_id": "look-right"},
        {"command": "lookDown", "key": "down", "code": "ArrowDown", "sequence_id": "look-down"},
    ] if commands == {"KeyW": "moveForward", "ArrowRight": "lookRight", "ArrowDown": "lookDown"} else [
        {"command": value, "key": key.removeprefix("Arrow").lower() if key.startswith("Arrow") else key.removeprefix("Key").lower(), "code": key, "sequence_id": value}
        for key, value in commands.items()
    ]


def parse_png_rgba(path: Path) -> tuple[int, int, list[bytes]]:
    data = path.read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError(f"{path} is not a PNG")
    offset = 8
    width = height = bit_depth = color_type = None
    idat = bytearray()
    while offset < len(data):
        if offset + 8 > len(data):
            raise ValueError("truncated PNG chunk header")
        length = struct.unpack(">I", data[offset:offset + 4])[0]
        chunk_type = data[offset + 4:offset + 8]
        chunk_data = data[offset + 8:offset + 8 + length]
        offset += 12 + length
        if chunk_type == b"IHDR":
            width, height, bit_depth, color_type, _compression, _filter, _interlace = struct.unpack(">IIBBBBB", chunk_data)
        elif chunk_type == b"IDAT":
            idat.extend(chunk_data)
        elif chunk_type == b"IEND":
            break
    if width is None or height is None or bit_depth is None or color_type is None:
        raise ValueError("PNG missing IHDR")
    if bit_depth != 8 or color_type not in (2, 6):
        raise ValueError(f"unsupported PNG format bit_depth={bit_depth} color_type={color_type}; expected RGB/RGBA 8-bit")
    channels = 3 if color_type == 2 else 4
    stride = width * channels
    raw = zlib.decompress(bytes(idat))
    rows: list[bytearray] = []
    pos = 0
    prev = bytearray(stride)
    for _y in range(height):
        filter_type = raw[pos]
        pos += 1
        row = bytearray(raw[pos:pos + stride])
        pos += stride
        recon = bytearray(stride)
        for i, value in enumerate(row):
            left = recon[i - channels] if i >= channels else 0
            up = prev[i]
            up_left = prev[i - channels] if i >= channels else 0
            if filter_type == 0:
                predictor = 0
            elif filter_type == 1:
                predictor = left
            elif filter_type == 2:
                predictor = up
            elif filter_type == 3:
                predictor = (left + up) // 2
            elif filter_type == 4:
                p = left + up - up_left
                pa, pb, pc = abs(p - left), abs(p - up), abs(p - up_left)
                predictor = left if pa <= pb and pa <= pc else up if pb <= pc else up_left
            else:
                raise ValueError(f"unsupported PNG filter {filter_type}")
            recon[i] = (value + predictor) & 0xFF
        rows.append(recon)
        prev = recon
    rgba_rows: list[bytes] = []
    if channels == 4:
        rgba_rows = [bytes(row) for row in rows]
    else:
        for row in rows:
            out = bytearray()
            for i in range(0, len(row), 3):
                out.extend(row[i:i + 3])
                out.append(255)
            rgba_rows.append(bytes(out))
    return width, height, rgba_rows


def inspect_png(path: Path) -> dict[str, Any]:
    width, height, rows = parse_png_rgba(path)
    extrema = [[255, 0], [255, 0], [255, 0], [255, 0]]
    unique: set[tuple[int, int, int, int]] = set()
    for row in rows:
        for i in range(0, len(row), 4):
            pixel = (row[i], row[i + 1], row[i + 2], row[i + 3])
            if len(unique) <= 64:
                unique.add(pixel)
            for channel, value in enumerate(pixel):
                extrema[channel][0] = min(extrema[channel][0], value)
                extrema[channel][1] = max(extrema[channel][1], value)
    blank = extrema[3][1] == 0 or (extrema[0][1] == 0 and extrema[1][1] == 0 and extrema[2][1] == 0 and extrema[3][1] == 0)
    return {
        "width": width,
        "height": height,
        "status": "blank" if blank else "visible",
        "classification": "blank-or-transparent-png" if blank else "visible-nonblank-png",
        "extrema": extrema,
        "uniqueColorsSampled": len(unique),
    }


def compare_pngs(before: Path, after: Path) -> dict[str, Any]:
    bw, bh, brows = parse_png_rgba(before)
    aw, ah, arows = parse_png_rgba(after)
    if (bw, bh) != (aw, ah):
        return {"status": "changed", "classification": "dimension-change", "beforeSize": [bw, bh], "afterSize": [aw, ah]}
    total_pixels = bw * bh
    changed_pixels = 0
    sample_delta = 0
    for brow, arow in zip(brows, arows):
        for i in range(0, len(brow), 4):
            if brow[i:i + 4] != arow[i:i + 4]:
                changed_pixels += 1
                sample_delta += sum(abs(brow[i + c] - arow[i + c]) for c in range(4))
    ratio = changed_pixels / total_pixels if total_pixels else 0.0
    status = "changed" if changed_pixels > 0 else "unchanged"
    return {
        "status": status,
        "classification": "visible-change" if status == "changed" else "no-pixel-change",
        "changedPixels": changed_pixels,
        "totalPixels": total_pixels,
        "changedPixelRatio": ratio,
        "absoluteChannelDelta": sample_delta,
    }


def capture(compositorctl: Path, session_id: str, session_token: str, surface_id: str, sequence_id: str) -> dict[str, Any]:
    return run_json([
        str(compositorctl), "--pretty", "capture",
        "--session", session_id,
        "--session-token", session_token,
        "--surface", surface_id,
        "--format", "png",
        "--export",
        "--evidence-class", "surface_screenshot",
        "--asha-command-sequence-id", sequence_id,
    ], timeout=20)


def expected_projection_for_step(asha_artifact: dict[str, Any], step: int) -> str | None:
    evidence = asha_artifact.get("cameraEvidence", {})
    if step <= 0:
        value = evidence.get("initial", {}).get("projectionHash")
    else:
        steps = evidence.get("steps", [])
        value = None
        if isinstance(steps, list) and step - 1 < len(steps):
            value = steps[step - 1].get("after", {}).get("projectionHash")
        if value is None and step >= len(steps):
            value = evidence.get("final", {}).get("projectionHash")
    return value if isinstance(value, str) and value else None


def flatten_a11y_text(node: dict[str, Any]) -> list[str]:
    texts: list[str] = []
    for key in ("name", "description", "role", "semantic_role"):
        value = node.get(key)
        if isinstance(value, str) and value.strip():
            texts.append(value.strip())
    children = node.get("children")
    if isinstance(children, list):
        for child in children:
            if isinstance(child, dict):
                texts.extend(flatten_a11y_text(child))
    return texts


def parse_readiness_markers(texts: list[str]) -> dict[str, Any]:
    haystack = "\n".join(texts)
    markers: dict[str, Any] = {"has_title": "ASHA First-Person Agora Control" in haystack}
    scenario_match = re.search(r'"scenarioId"\s*:\s*"([^"]+)"', haystack) or re.search(r"Scenario:\s*([a-zA-Z0-9_.:-]+)", haystack)
    if scenario_match:
        markers["scenario_id"] = scenario_match.group(1)
    step_match = re.search(r'"step"\s*:\s*(\d+)', haystack)
    if step_match:
        markers["step"] = int(step_match.group(1))
    projection_match = re.search(r'"projectionHash"\s*:\s*"([^"]+)"', haystack) or re.search(r"projectionHash\s+([^\s,}]+)", haystack)
    if projection_match:
        markers["projection_hash"] = projection_match.group(1)
    markers["text_sample"] = haystack[:1200]
    return markers


def evaluate_marker_readiness(markers: dict[str, Any], expectation: PageReadinessExpectation, *, backend: str) -> dict[str, Any]:
    failures: list[str] = []
    title = markers.get("title")
    if title is not None and title != "ASHA First-Person Agora Control":
        failures.append(f"title {title!r} != 'ASHA First-Person Agora Control'")
    ready_value = markers.get("bodyReady")
    if ready_value not in ("true", True):
        failures.append(f"bodyReady marker is {ready_value!r}, expected true")
    if markers.get("scenario_id") is None and markers.get("scenarioId") is not None:
        markers["scenario_id"] = markers.get("scenarioId")
    if markers.get("projection_hash") is None and markers.get("projectionHash") is not None:
        markers["projection_hash"] = markers.get("projectionHash")
    if markers.get("step") is None and markers.get("bodyStep") not in (None, ""):
        raw_step = markers.get("bodyStep")
        try:
            markers["step"] = int(str(raw_step))
        except (TypeError, ValueError):
            failures.append(f"step marker {raw_step!r} is not an integer")
    if markers.get("scenario_id") != expectation.scenario_id:
        failures.append(f"scenario id {markers.get('scenario_id')!r} != {expectation.scenario_id!r}")
    if markers.get("step") != expectation.step:
        failures.append(f"step {markers.get('step')!r} != {expectation.step!r}")
    if expectation.projection_hash and markers.get("projection_hash") != expectation.projection_hash:
        failures.append(f"projectionHash {markers.get('projection_hash')!r} != {expectation.projection_hash!r}")
    result = {
        "status": "ready" if not failures else "not_ready",
        "phase": expectation.phase,
        "backend": backend,
        "expected": {
            "scenarioId": expectation.scenario_id,
            "step": expectation.step,
            "projectionHash": expectation.projection_hash,
        },
        "markers": markers,
        "failures": failures,
    }
    if failures:
        raise ProofReadinessError("surface_mapped_page_not_ready", "; ".join(failures), result)
    return result


def evaluate_page_readiness(a11y_response: dict[str, Any], expectation: PageReadinessExpectation) -> dict[str, Any]:
    root = a11y_response.get("root")
    if not isinstance(root, dict):
        raise ProofReadinessError("surface_mapped_page_not_ready", "a11y semantic response did not include a root node", {"response": a11y_response})
    texts = flatten_a11y_text(root)
    markers = parse_readiness_markers(texts)
    failures: list[str] = []
    if not markers.get("has_title"):
        failures.append("missing ASHA proof page title")
    if markers.get("scenario_id") != expectation.scenario_id:
        failures.append(f"scenario id {markers.get('scenario_id')!r} != {expectation.scenario_id!r}")
    if markers.get("step") != expectation.step:
        failures.append(f"step {markers.get('step')!r} != {expectation.step!r}")
    if expectation.projection_hash and markers.get("projection_hash") != expectation.projection_hash:
        failures.append(f"projectionHash {markers.get('projection_hash')!r} != {expectation.projection_hash!r}")
    result = {
        "status": "ready" if not failures else "not_ready",
        "phase": expectation.phase,
        "backend": a11y_response.get("backend"),
        "expected": {
            "scenarioId": expectation.scenario_id,
            "step": expectation.step,
            "projectionHash": expectation.projection_hash,
        },
        "markers": markers,
        "failures": failures,
    }
    if failures:
        raise ProofReadinessError("surface_mapped_page_not_ready", "; ".join(failures), result)
    return result


def read_page_readiness_via_app_command(compositorctl: Path, surface_id: str, session_id: str, session_token: str, expectation: PageReadinessExpectation) -> dict[str, Any]:
    response = run_json([
        str(compositorctl), "--pretty", "app", "command",
        "--surface", surface_id,
        "--session", session_id,
        "--session-token", session_token,
        "--command", json.dumps({"type": "readiness", "scenarioId": expectation.scenario_id}),
        "--timeout-ms", "3000",
    ], timeout=20)
    result = response.get("result")
    if not isinstance(result, dict) or not result.get("ok"):
        raise ProofReadinessError("surface_mapped_page_not_ready", "app-command readiness response was not ok", {"response": response})
    markers = result.get("readiness")
    if not isinstance(markers, dict):
        raise ProofReadinessError("surface_mapped_page_not_ready", "app-command readiness response did not include readiness markers", {"response": response})
    markers = dict(markers)
    markers["request_id"] = response.get("request_id")
    return evaluate_marker_readiness(markers, expectation, backend="webview-app-command")


def assert_page_readiness(compositorctl: Path, surface_id: str, expectation: PageReadinessExpectation, *, session_id: str = "", session_token: str = "") -> dict[str, Any]:
    if not surface_id:
        raise ProofReadinessError("no_surface", "launch response did not include a surface id")
    app_command_error: str | None = None
    if session_id and session_token:
        try:
            return read_page_readiness_via_app_command(compositorctl, surface_id, session_id, session_token, expectation)
        except ProofReadinessError:
            raise
        except RuntimeError as exc:
            app_command_error = str(exc)
    try:
        response = run_json([str(compositorctl), "--pretty", "a11y", "semantic", "--surface", surface_id, "--depth", "14"], timeout=20)
    except RuntimeError as exc:
        detail = f"a11y semantic readiness query failed: {exc}"
        if app_command_error:
            detail = f"app-command readiness failed: {app_command_error}; {detail}"
        raise ProofReadinessError("surface_mapped_page_not_ready", detail) from exc
    return evaluate_page_readiness(response, expectation)


def assert_capture_has_ready_proof_content(readiness: dict[str, Any], inspection: dict[str, Any], expectation: PageReadinessExpectation) -> None:
    if inspection.get("status") == "visible" and readiness.get("status") != "ready":
        raise ProofReadinessError("capture_visible_proof_content_missing", "capture is visible but proof readiness markers are missing", {"readiness": readiness, "inspection": inspection, "expected": expectation.__dict__})


def assert_state_advanced(before: dict[str, Any], after: dict[str, Any], command: str) -> None:
    before_markers = before.get("markers", {})
    after_markers = after.get("markers", {})
    if after_markers.get("step") == before_markers.get("step") or after_markers.get("projection_hash") == before_markers.get("projection_hash"):
        raise ProofReadinessError(
            "input_accepted_state_did_not_advance",
            f"input for {command} was accepted but page state markers did not advance",
            {"before": before_markers, "after": after_markers, "command": command},
        )


def free_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def maybe_sleep(seconds: float) -> None:
    if seconds > 0:
        time.sleep(seconds)


def capture_image_path(capture_response: dict[str, Any]) -> Path:
    raw = capture_response.get("image_path") or capture_response.get("path")
    if not isinstance(raw, str) or not raw:
        raise RuntimeError(f"capture response did not include image_path/path: {capture_response}")
    return Path(raw)


def run_live(args: argparse.Namespace) -> int:
    asha_demo_root = Path(args.asha_demo_root).resolve()
    compositorctl = Path(args.compositorctl)
    webview_launcher = Path(args.webview_launcher)
    if args.require_clean_agora_repo:
        require_clean_or_explain(Path(args.agora_repo_root).resolve())
    if args.require_clean_asha_demo:
        require_clean_or_explain(asha_demo_root)

    run(["npm", "run", "camera:agora-control"], cwd=asha_demo_root, timeout=60)
    asha_artifact = load_asha_artifact(asha_demo_root)
    commands = command_sequence(asha_artifact)
    page_path = asha_demo_root / "harness/out/camera-agora-control/latest/index.html"
    if not page_path.exists():
        raise RuntimeError(f"generated page is missing: {page_path}")

    agora_repo = Path(args.agora_repo_root).resolve()
    agora_commit = git_output(agora_repo, "rev-parse", "HEAD")
    agora_branch = git_output(agora_repo, "branch", "--show-current")
    audit_id = f"agora-os-{TASK_ID}-{int(time.time())}"

    session = run_json([
        str(compositorctl), "--pretty", "session", "create",
        "--label", SCENARIO_ID,
        "--project-id", PROJECT_ID,
        "--task-id", str(TASK_ID),
        "--agent-identity", AGENT_IDENTITY,
        "--asha-scenario", SCENARIO_ID,
        "--repo-commit", agora_commit,
        "--repo-branch", agora_branch,
        "--asha-runtime-mode", asha_artifact.get("runtime", {}).get("mode", ""),
        "--artifact-root", str(asha_demo_root / "harness/out/camera-agora-control/latest"),
        "--audit-correlation-id", audit_id,
    ])
    session_id = session["session_id"]
    session_token = session["session_token"]
    cleanup_state: dict[str, Any] = {"done": False, "result": None}

    def cleanup_session_once() -> dict[str, Any] | None:
        if args.keep_session_running:
            return None
        if cleanup_state["done"]:
            result = cleanup_state["result"]
            return result if isinstance(result, dict) else None
        cleanup_state["done"] = True
        try:
            cleanup_result = run([str(compositorctl), "--pretty", "session", "destroy", "--session", session_id], timeout=20)
            cleanup_state["result"] = {"status": "destroyed", "stdout": cleanup_result.stdout.strip()}
        except Exception as exc:
            cleanup_state["result"] = {"status": "failed", "error": str(exc)}
        result = cleanup_state["result"]
        return result if isinstance(result, dict) else None

    atexit.register(cleanup_session_once)

    app_command_port = free_loopback_port()
    launch_cmd = " ".join([
        shlex.quote(str(webview_launcher)),
        "--path", shlex.quote(str(page_path)),
        "--title", shlex.quote("ASHA First-Person Agora Control"),
        "--app-id", shlex.quote("org.agora.ASHAFirstPersonControl"),
        "--width", str(args.width),
        "--height", str(args.height),
        "--app-command-port", str(app_command_port),
    ])
    launch = run_json([
        str(compositorctl), "--pretty", "launch",
        "--session", session_id,
        "--session-token", session_token,
        "--cwd", str(asha_demo_root),
        "--cmd", launch_cmd,
        "--expected-title", "ASHA First-Person Agora Control",
        "--wait-surface",
        "--wait-timeout-ms", str(args.wait_timeout_ms),
    ], timeout=max(30, args.wait_timeout_ms // 1000 + 5))
    surface = launch.get("surface") or {}
    surface_id = surface.get("surface", {}).get("id") or surface.get("id")
    if not surface_id:
        raise ProofReadinessError("no_surface", f"launch response did not include surface id: {launch}")

    readiness_notes: list[str] = []
    try:
        run_json([str(compositorctl), "--pretty", "wait", "for-render-idle", "--surface", surface_id, "--idle-ms", "500", "--timeout", str(args.wait_timeout_ms)], timeout=20)
    except RuntimeError as exc:
        readiness_notes.append(f"initial render-idle wait timed out; continuing to capture because surface is mapped/capturable: {exc}")
    maybe_sleep(args.settle_seconds)
    before = capture(compositorctl, session_id, session_token, surface_id, "initial")
    before_path = capture_image_path(before)
    before_inspection = inspect_png(before_path)
    initial_expectation = PageReadinessExpectation(
        scenario_id=SCENARIO_ID,
        step=0,
        projection_hash=expected_projection_for_step(asha_artifact, 0),
        phase="initial",
    )
    try:
        before_readiness = assert_page_readiness(compositorctl, surface_id, initial_expectation, session_id=session_id, session_token=session_token)
    except ProofReadinessError as exc:
        if before_inspection["status"] == "visible":
            raise ProofReadinessError("capture_visible_proof_content_missing", str(exc), {"readiness": exc.details, "inspection": before_inspection}) from exc
        raise
    assert_capture_has_ready_proof_content(before_readiness, before_inspection, initial_expectation)

    step_results = []
    previous_readiness = before_readiness
    for index, command in enumerate(commands, start=1):
        before_frame = surface.get("frame_count", 0) if isinstance(surface, dict) else 0
        key_result = run_json([
            str(compositorctl), "--pretty", "key",
            "--session", session_id,
            "--session-token", session_token,
            "--surface", surface_id,
            "--key", command["key"],
        ], timeout=20)
        accepted = int(key_result.get("accepted") or 0)
        rejected = int(key_result.get("rejected") or 0)
        if accepted < 2 or rejected != 0:
            raise RuntimeError(f"key injection for {command['command']} did not fully succeed: accepted={accepted} rejected={rejected}")
        try:
            wait_frame = run_json([str(compositorctl), "--pretty", "wait", "for-frame", "--surface", surface_id, "--after-frame", str(before_frame), "--timeout", "3000"], timeout=10)
        except RuntimeError:
            wait_frame = {"ok": False, "note": "frame wait timed out after injected key"}
        try:
            run_json([str(compositorctl), "--pretty", "wait", "for-render-idle", "--surface", surface_id, "--idle-ms", "250", "--timeout", "5000"], timeout=10)
        except RuntimeError as exc:
            readiness_notes.append(f"render-idle wait after {command['command']} timed out; capture still attempted: {exc}")
        maybe_sleep(args.settle_seconds)
        cap = capture(compositorctl, session_id, session_token, surface_id, f"after-{index}-{command['sequence_id']}")
        cap_path = capture_image_path(cap)
        cap_inspection = inspect_png(cap_path)
        expectation = PageReadinessExpectation(
            scenario_id=SCENARIO_ID,
            step=index,
            projection_hash=expected_projection_for_step(asha_artifact, index),
            phase=f"after-{index}-{command['sequence_id']}",
        )
        try:
            readiness = assert_page_readiness(compositorctl, surface_id, expectation, session_id=session_id, session_token=session_token)
        except ProofReadinessError as exc:
            if cap_inspection["status"] == "visible":
                raise ProofReadinessError("capture_visible_proof_content_missing", str(exc), {"readiness": exc.details, "inspection": cap_inspection}) from exc
            raise
        assert_capture_has_ready_proof_content(readiness, cap_inspection, expectation)
        assert_state_advanced(previous_readiness, readiness, command["command"])
        step_results.append({"index": index, "command": command, "keyResult": key_result, "waitFrame": wait_frame, "capture": cap, "inspection": cap_inspection, "readiness": readiness})
        previous_readiness = readiness

    after = step_results[-1]["capture"] if step_results else before
    before_path = capture_image_path(before)
    after_path = capture_image_path(after)
    before_inspection = inspect_png(before_path)
    after_inspection = inspect_png(after_path)
    visual_change = compare_pngs(before_path, after_path)
    if before_inspection["status"] != "visible" or after_inspection["status"] != "visible":
        comparison = "unavailable"
        reason = "blank-or-transparent-capture"
    elif visual_change["status"] != "changed":
        comparison = "mismatched"
        reason = "agent-input-produced-no-pixel-change"
    else:
        comparison = "comparable"
        reason = "before-after-visible-change-after-agent-input"

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
            "launchId": launch["launch_id"],
            "surfaceId": surface_id,
            "auditCorrelationId": audit_id,
            "appCommandPort": app_command_port,
        },
        "ashaDemo": {
            "repo": str(asha_demo_root),
            "branch": asha_artifact.get("repo", {}).get("branch"),
            "commit": asha_artifact.get("repo", {}).get("commit"),
            "artifact": str(asha_demo_root / "harness/out/camera-agora-control/latest/index.json"),
            "page": str(page_path),
            "initialProjectionHash": asha_artifact.get("cameraEvidence", {}).get("initial", {}).get("projectionHash"),
            "finalProjectionHash": asha_artifact.get("cameraEvidence", {}).get("final", {}).get("projectionHash"),
        },
        "control": {
            "route": "Agora compositor keyboard injection to ASHA page public keyboard handler",
            "commands": commands,
            "keyResults": [{"command": step["command"], "accepted": step["keyResult"].get("accepted"), "rejected": step["keyResult"].get("rejected")} for step in step_results],
        },
        "readiness": {
            "policy": "surface must map, capture must be visible/nonblank, accessibility text must expose the ASHA proof title, scenario id, step, and projectionHash markers, input must advance those markers, and before/after pixels must differ; render-idle wait is advisory because some WebKit/Wayfire paths keep frame_count at zero",
            "backend": before_readiness.get("backend"),
            "initial": before_readiness,
            "steps": [{"command": step["command"], "readiness": step["readiness"]} for step in step_results],
            "notes": readiness_notes,
        },
        "captures": {
            "before": before,
            "steps": [{"command": step["command"], "capture": step["capture"], "inspection": step["inspection"]} for step in step_results],
            "after": after,
            "beforeInspection": before_inspection,
            "afterInspection": after_inspection,
            "visualChange": visual_change,
        },
        "comparison": {"status": comparison, "reason": reason},
        "cleanup": {
            "sessionDestroyedAfterCapture": not args.keep_session_running,
            "result": cleanup,
        },
    }
    out_path = asha_demo_root / "harness/out/camera-agora-control/latest/agora-control-proof.json"
    out_path.write_text(json.dumps(proof, indent=2) + "\n")
    print(json.dumps({
        "proof": str(out_path),
        "comparison": proof["comparison"],
        "session_id": session_id,
        "launch_id": launch["launch_id"],
        "surface_id": surface_id,
        "before_capture": before.get("artifact", {}).get("artifact_id") or before.get("request_id"),
        "after_capture": after.get("artifact", {}).get("artifact_id") or after.get("request_id"),
        "before_image": str(before_path),
        "after_image": str(after_path),
        "changed_pixel_ratio": visual_change.get("changedPixelRatio"),
    }, indent=2))
    return 0 if comparison == "comparable" else 2


def self_test() -> int:
    import tempfile
    # 2x1 RGBA PNGs generated with filter type 0.
    def png(path: Path, pixels: bytes) -> None:
        raw = b"\x00" + pixels
        def chunk(kind: bytes, payload: bytes) -> bytes:
            crc = zlib.crc32(kind + payload) & 0xffffffff
            return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", crc)
        data = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 2, 1, 8, 6, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
        path.write_bytes(data)
    with tempfile.TemporaryDirectory() as tmp:
        before = Path(tmp) / "before.png"
        after = Path(tmp) / "after.png"
        png(before, bytes([0, 0, 0, 255, 255, 255, 255, 255]))
        png(after, bytes([0, 0, 0, 255, 200, 200, 255, 255]))
        assert inspect_png(before)["status"] == "visible"
        diff = compare_pngs(before, after)
        assert diff["status"] == "changed", diff
        assert diff["changedPixels"] == 1, diff

    ready_tree = {
        "backend": "self-test",
        "root": {
            "name": "ASHA First-Person Agora Control",
            "role": "document",
            "children": [
                {"name": "Scenario: first-person-agora-control-basic"},
                {"name": '{"scenarioId":"first-person-agora-control-basic","step":0,"projectionHash":"fnv1a64:initial"}'},
            ],
        },
    }
    ready = evaluate_page_readiness(
        ready_tree,
        PageReadinessExpectation(SCENARIO_ID, step=0, projection_hash="fnv1a64:initial", phase="self-test"),
    )
    assert ready["status"] == "ready", ready
    advanced_tree = {
        "backend": "self-test",
        "root": {
            "name": "ASHA First-Person Agora Control",
            "role": "document",
            "children": [
                {"name": '{"scenarioId":"first-person-agora-control-basic","step":1,"projectionHash":"fnv1a64:after"}'},
            ],
        },
    }
    advanced = evaluate_page_readiness(
        advanced_tree,
        PageReadinessExpectation(SCENARIO_ID, step=1, projection_hash="fnv1a64:after", phase="self-test-after"),
    )
    assert_state_advanced(ready, advanced, "moveForward")
    app_markers = {
        "title": "ASHA First-Person Agora Control",
        "bodyReady": "true",
        "scenarioId": SCENARIO_ID,
        "step": 0,
        "projectionHash": "fnv1a64:initial",
    }
    app_ready = evaluate_marker_readiness(dict(app_markers), PageReadinessExpectation(SCENARIO_ID, 0, "fnv1a64:initial"), backend="self-test-app-command")
    assert app_ready["status"] == "ready", app_ready
    for missing_ready in (dict(app_markers, bodyReady=""), {k: v for k, v in app_markers.items() if k != "bodyReady"}, {**{k: v for k, v in app_markers.items() if k != "bodyReady"}, "ready": True}):
        try:
            evaluate_marker_readiness(missing_ready, PageReadinessExpectation(SCENARIO_ID, 0, "fnv1a64:initial"), backend="self-test-app-command")
        except ProofReadinessError as exc:
            assert exc.category == "surface_mapped_page_not_ready", exc
        else:
            raise AssertionError("expected app-command readiness failure when bodyReady is absent or false")
    for bad_tree, category in [
        ({"backend": "self-test", "root": {"name": "ASHA First-Person Agora Control", "children": [{"name": '{"scenarioId":"wrong","step":0,"projectionHash":"fnv1a64:initial"}'}]}}, "surface_mapped_page_not_ready"),
        ({"backend": "self-test"}, "surface_mapped_page_not_ready"),
    ]:
        try:
            evaluate_page_readiness(bad_tree, PageReadinessExpectation(SCENARIO_ID, step=0, projection_hash="fnv1a64:initial"))
        except ProofReadinessError as exc:
            assert exc.category == category, exc
        else:
            raise AssertionError("expected readiness failure")
    try:
        assert_state_advanced(ready, ready, "moveForward")
    except ProofReadinessError as exc:
        assert exc.category == "input_accepted_state_did_not_advance", exc
    else:
        raise AssertionError("expected state advancement failure")
    print("self-test passed")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--asha-demo-root", default=str(DEFAULT_ASHA_DEMO_ROOT))
    parser.add_argument("--agora-repo-root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--compositorctl", default=str(DEFAULT_COMPOSITORCTL))
    parser.add_argument("--webview-launcher", default=str(DEFAULT_WEBVIEW_LAUNCHER))
    parser.add_argument("--width", type=int, default=920)
    parser.add_argument("--height", type=int, default=720)
    parser.add_argument("--wait-timeout-ms", type=int, default=15000)
    parser.add_argument("--settle-seconds", type=float, default=0.25)
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
    except Exception as exc:  # keep CLI failure concise for Den task evidence
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
