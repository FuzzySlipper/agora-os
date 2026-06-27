#!/usr/bin/env python3
"""Run the Agora split-shell live visibility canary.

The canary launches split background/dock surfaces and ordinary app windows, but
it no longer treats mapped dock readback as human-facing success.  By default it
requires dock presentation evidence (frame/present readback, successful dock
capture, or an explicit physical observation receipt) before passing.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import signal
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Any

SHELL_APP_IDS = {
    "io.agoraos.ShellBackground",
    "io.agoraos.ShellDock",
}


@dataclass
class CommandResult:
    command: list[str] | str
    returncode: int
    stdout: str
    stderr: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "command": self.command,
            "returncode": self.returncode,
            "stdout": self.stdout,
            "stderr": self.stderr,
        }


class CanaryError(RuntimeError):
    pass


class Canary:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.started_at = dt.datetime.now(dt.timezone.utc).isoformat()
        stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        self.out_dir = pathlib.Path(args.output_dir or f"/tmp/agora-split-shell-canary/{stamp}")
        self.out_dir.mkdir(parents=True, exist_ok=True)
        self.commands: dict[str, Any] = {}
        self.launches: dict[str, dict[str, Any]] = {}
        self.surface_captures: dict[str, Any] = {}
        self.screenshots: dict[str, Any] = {}
        self.supervisor: subprocess.Popen[str] | None = None
        self.panel_was_active = False
        self.panel_stopped = False
        self.minimized_fallback_shells: list[str] = []
        self.failures: list[str] = []
        self.physical_observation = self.load_physical_observation()

    def run(self, command: list[str], *, check: bool = True, env: dict[str, str] | None = None, timeout: int = 60) -> CommandResult:
        proc = subprocess.run(command, text=True, capture_output=True, env=env, timeout=timeout)
        result = CommandResult(command, proc.returncode, proc.stdout, proc.stderr)
        if check and proc.returncode != 0:
            raise CanaryError(f"command failed ({proc.returncode}): {' '.join(command)}\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}")
        return result

    def shell(self, command: str, *, check: bool = True, timeout: int = 60) -> CommandResult:
        proc = subprocess.run(command, shell=True, text=True, capture_output=True, timeout=timeout, executable="/usr/bin/bash")
        result = CommandResult(command, proc.returncode, proc.stdout, proc.stderr)
        if check and proc.returncode != 0:
            raise CanaryError(f"command failed ({proc.returncode}): {command}\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}")
        return result

    def list_surfaces(self, label: str) -> dict[str, Any]:
        result = self.run([self.args.compositorctl, "--pretty", "list-surfaces"])
        self.commands[f"list_surfaces_{label}"] = result.as_dict()
        path = self.out_dir / f"list-surfaces-{label}.json"
        path.write_text(result.stdout, encoding="utf-8")
        return json.loads(result.stdout)

    @staticmethod
    def surfaces(data: dict[str, Any]) -> list[dict[str, Any]]:
        return [item.get("surface") or {} for item in data.get("surfaces", [])]

    def find_by_app_id(self, data: dict[str, Any], app_id: str) -> dict[str, Any] | None:
        for item in data.get("surfaces", []):
            surface = item.get("surface") or {}
            if surface.get("app_id") == app_id:
                return item
        return None

    def systemctl(self, *args: str, check: bool = True) -> CommandResult:
        return self.run(["sudo", "-n", "systemctl", *args], check=check, timeout=90)

    def stop_panel_service(self) -> None:
        active = self.systemctl("is-active", "agora-shell-panel.service", check=False)
        self.commands["panel_is_active_before"] = active.as_dict()
        self.panel_was_active = active.stdout.strip() == "active"
        if self.args.manage_panel_service and self.panel_was_active:
            stopped = self.systemctl("stop", "agora-shell-panel.service", check=False)
            self.commands["panel_stop"] = stopped.as_dict()
            if stopped.returncode != 0:
                self.minimize_visible_fallback_shells("panel-stop-unavailable")
                return
            self.panel_stopped = True
            deadline = time.time() + 10
            while time.time() < deadline:
                state = self.systemctl("is-active", "agora-shell-panel.service", check=False)
                if state.stdout.strip() != "active":
                    self.commands["panel_state_after_stop"] = state.as_dict()
                    return
                time.sleep(0.25)
            raise CanaryError("agora-shell-panel.service remained active after stop")

    def minimize_visible_fallback_shells(self, label: str) -> None:
        """Hide the deployed fullscreen fallback when sudo cannot stop the service.

        This preserves the production service while ensuring the split canary does
        not rely on a fullscreen xdg shell sitting in the workspace stack.
        """
        data = self.list_surfaces(label)
        for surface in self.surfaces(data):
            if surface.get("app_id") == "io.agoraos.ShellPanel" and surface.get("role") == "toplevel" and surface.get("visible") is True:
                surface_id = surface.get("id")
                if isinstance(surface_id, str) and surface_id:
                    result = self.run([self.args.compositorctl, "--pretty", "surface", "minimize", "--surface", surface_id, "--state", "true"], check=False, timeout=15)
                    self.commands[f"minimize_fallback_{surface_id}"] = result.as_dict()
                    if result.returncode == 0:
                        self.minimized_fallback_shells.append(surface_id)

    def restore_panel_service(self) -> None:
        for surface_id in self.minimized_fallback_shells:
            result = self.run([self.args.compositorctl, "--pretty", "surface", "minimize", "--surface", surface_id, "--state", "false"], check=False, timeout=15)
            self.commands[f"restore_fallback_{surface_id}"] = result.as_dict()
        if self.panel_stopped and self.args.restore_panel_service:
            started = self.systemctl("start", "agora-shell-panel.service", check=False)
            self.commands["panel_start_restore"] = started.as_dict()
            active = self.systemctl("is-active", "agora-shell-panel.service", check=False)
            self.commands["panel_is_active_after_restore"] = active.as_dict()

    def launch_app(self, name: str, app_id: str, title: str) -> dict[str, Any]:
        cmd = (
            f"foot --app-id {app_id} --title {title} "
            "sh -lc 'printf "
            + json.dumps(f"{title}\\nThis is ordinary xdg app window {name}.\\n")
            + "; sleep 600'"
        )
        result = self.run([
            self.args.compositorctl,
            "--pretty",
            "launch",
            "--cmd",
            cmd,
            "--expected-app-id",
            app_id,
            "--expected-title",
            title,
            "--wait-surface",
            "--wait-timeout-ms",
            str(self.args.wait_timeout_ms),
        ], timeout=30)
        self.commands[f"launch_{name}"] = result.as_dict()
        data = json.loads(result.stdout)
        self.launches[name] = data
        return data

    def move_surface(self, label: str, surface_id: str, x: int, y: int, width: int, height: int) -> dict[str, Any]:
        result = self.run([
            self.args.compositorctl,
            "--pretty",
            "surface",
            "move",
            "--surface",
            surface_id,
            "--x",
            str(x),
            "--y",
            str(y),
            "--width",
            str(width),
            "--height",
            str(height),
        ], timeout=15)
        self.commands[f"move_{label}"] = result.as_dict()
        return json.loads(result.stdout)


    def tile_surface(self, label: str, surface_id: str, row: int, col: int) -> dict[str, Any]:
        result = self.run([
            self.args.compositorctl,
            "--pretty",
            "surface",
            "tile",
            "--surface",
            surface_id,
            "--row",
            str(row),
            "--col",
            str(col),
            "--timeout-ms",
            "8000",
        ], timeout=15)
        self.commands[f"tile_{label}"] = result.as_dict()
        return json.loads(result.stdout)

    def start_split_supervisor(self) -> None:
        env = os.environ.copy()
        env.update({
            "AGORA_SHELL_MODE": "split",
            "AGORA_SHELL_POLL_INTERVAL_SECONDS": "0.5",
            "AGORA_SHELL_WAIT_TIMEOUT_MS": str(self.args.wait_timeout_ms),
        })
        if self.args.supervisor:
            supervisor = self.args.supervisor
        else:
            supervisor = "/usr/local/bin/agora-shell-panel-supervisor"
        log_path = self.out_dir / "split-supervisor.log"
        log_handle = log_path.open("w", encoding="utf-8")
        self.supervisor = subprocess.Popen([supervisor], text=True, stdout=log_handle, stderr=subprocess.STDOUT, env=env)
        self.commands["split_supervisor_start"] = {"command": [supervisor], "pid": self.supervisor.pid, "log": str(log_path)}

    def split_surface_ids_from_log(self) -> dict[str, str]:
        log_ref = self.commands.get("split_supervisor_start", {}).get("log")
        ids: dict[str, str] = {}
        if not isinstance(log_ref, str):
            return ids
        path = pathlib.Path(log_ref)
        if not path.exists():
            return ids
        text = path.read_text(encoding="utf-8", errors="replace")
        for name, surface_id in re.findall(r"shell (background|dock) mapped launch_id=\S+ surface_id=(\S+)", text):
            ids[name] = surface_id
        return ids

    def wait_for_split_surfaces(self) -> dict[str, Any]:
        deadline = time.time() + self.args.wait_timeout_ms / 1000
        last: dict[str, Any] | None = None
        while time.time() < deadline:
            data = self.list_surfaces("split-wait")
            last = data
            mapped = self.split_surface_ids_from_log()
            present = {s.get("id") for s in self.surfaces(data)}
            if mapped.get("background") in present and mapped.get("dock") in present:
                return data
            # Fallback for older/newer supervisors where log parsing changes: accept
            # two visible layer-shell surfaces with background/dock-like geometry.
            layer_shells = [s for s in self.surfaces(data) if s.get("surface_kind") == "layer_shell" and s.get("visible") is True]
            have_background = any((s.get("geometry") or {}).get("height", 0) >= 1000 and ((s.get("layer_shell") or {}).get("exclusive_zone") is False) for s in layer_shells)
            have_dock = any(0 < (s.get("geometry") or {}).get("height", 0) <= 160 and ((s.get("layer_shell") or {}).get("exclusive_zone") is True) for s in layer_shells)
            if have_background and have_dock:
                return data
            if self.supervisor and self.supervisor.poll() is not None:
                raise CanaryError(f"split supervisor exited early with {self.supervisor.returncode}")
            time.sleep(0.5)
        raise CanaryError(f"timed out waiting for split shell surfaces; last={json.dumps(last)[:1000]}")

    def focus_and_raise(self, label: str, surface_id: str) -> dict[str, Any]:
        focus = self.run([self.args.compositorctl, "--pretty", "surface", "focus", "--surface", surface_id], timeout=15)
        raise_res = self.run([self.args.compositorctl, "--pretty", "surface", "raise", "--surface", surface_id], timeout=15)
        self.commands[f"focus_{label}"] = focus.as_dict()
        self.commands[f"raise_{label}"] = raise_res.as_dict()
        return {"focus": json.loads(focus.stdout), "raise": json.loads(raise_res.stdout)}

    def capture_surface(self, label: str, surface_id: str, *, required: bool) -> None:
        result = self.run([
            self.args.compositorctl,
            "--pretty",
            "capture",
            "--surface",
            surface_id,
            "--evidence-class",
            "desktop_behavior",
        ], check=False, timeout=20)
        self.commands[f"capture_{label}"] = result.as_dict()
        if result.returncode != 0:
            self.surface_captures[label] = {"ok": False, "error": result.stderr or result.stdout}
            if required:
                raise CanaryError(f"required capture failed for {label}: {result.stderr or result.stdout}")
            return
        data = json.loads(result.stdout)
        self.surface_captures[label] = {"ok": True, "response": data}

    def load_physical_observation(self) -> dict[str, Any]:
        if not self.args.physical_observation_file:
            return {}
        path = pathlib.Path(self.args.physical_observation_file)
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except Exception as exc:
            raise CanaryError(f"failed to read physical observation file {path}: {exc}") from exc

    @staticmethod
    def is_split_dock_surface(surface: dict[str, Any]) -> bool:
        geom = surface.get("geometry") or {}
        layer = surface.get("layer_shell") or {}
        return (
            surface.get("surface_kind") == "layer_shell"
            and surface.get("visible") is True
            and 0 < geom.get("height", 9999) <= 160
            and layer.get("exclusive_zone") is True
        )

    def evaluate_dock_presentation(self, data: dict[str, Any]) -> dict[str, Any]:
        """Evaluate whether the split dock has presentation evidence.

        Layer-shell map/readback alone is not enough for human-facing default
        deployment: Patch has observed mapped dock readback while the physical
        monitor shows only the background marker. This gate therefore requires
        either compositor presentation counters/timestamps, successful direct
        dock capture, or an explicit human physical observation receipt.
        """
        docks = [s for s in self.surfaces(data) if self.is_split_dock_surface(s)]
        evidence: dict[str, Any] = {
            "required": self.args.require_dock_presentation_evidence,
            "dock_count": len(docks),
            "signals": [],
            "manual_observation": self.physical_observation,
            "manual_observation_errors": [],
        }
        if not docks:
            evidence["verdict"] = "missing_dock_surface"
            return evidence
        dock = docks[0]
        evidence["dock_surface"] = {
            "id": dock.get("id"),
            "app_id": dock.get("app_id"),
            "title": dock.get("title"),
            "output_id": dock.get("output_id"),
            "role": dock.get("role"),
            "surface_kind": dock.get("surface_kind"),
            "geometry": dock.get("geometry"),
            "layer_shell": dock.get("layer_shell"),
            "frame_count": dock.get("frame_count"),
            "last_present_timestamp": dock.get("last_present_timestamp"),
            "capture_count": dock.get("capture_count"),
            "last_capture_timestamp": dock.get("last_capture_timestamp"),
            "capturable": dock.get("capturable"),
        }
        frame_count = dock.get("frame_count")
        if isinstance(frame_count, (int, float)) and frame_count > 0:
            evidence["signals"].append("frame_count")
        if dock.get("last_present_timestamp"):
            evidence["signals"].append("last_present_timestamp")
        dock_capture = self.surface_captures.get("dock") or {}
        evidence["dock_capture"] = dock_capture
        if dock_capture.get("ok"):
            visual = ((dock_capture.get("response") or {}).get("visual_inspection") or {}).get("status")
            if visual == "visible":
                evidence["signals"].append("dock_capture_visible")
        manual_errors = self.validate_physical_observation(dock)
        evidence["manual_observation_errors"] = manual_errors
        if self.physical_observation.get("dock_visible") is True and not manual_errors:
            evidence["signals"].append("manual_physical_observation")
        evidence["verdict"] = "presented" if evidence["signals"] else "mapped_without_presentation_evidence"
        return evidence

    def validate_physical_observation(self, dock: dict[str, Any]) -> list[str]:
        if not self.physical_observation:
            return []
        errors: list[str] = []
        if self.physical_observation.get("dock_visible") is not True:
            errors.append("dock_visible must be true")
        output = self.physical_observation.get("output") or self.physical_observation.get("output_id")
        dock_output = dock.get("output_id") or "HDMI-A-1"
        if output != dock_output:
            errors.append(f"output must match dock output {dock_output!r}")
        for field in ("observed_by", "observed_at"):
            value = self.physical_observation.get(field)
            if not isinstance(value, str) or not value.strip():
                errors.append(f"{field} is required")
        observed_at = self.physical_observation.get("observed_at")
        if isinstance(observed_at, str) and observed_at.strip():
            try:
                dt.datetime.fromisoformat(observed_at.replace("Z", "+00:00"))
            except ValueError:
                errors.append("observed_at must be ISO-8601 parseable")
        receipt_surface_id = self.physical_observation.get("dock_surface_id")
        if receipt_surface_id is not None and receipt_surface_id != dock.get("id"):
            errors.append(f"dock_surface_id must match current dock surface {dock.get('id')!r}")
        artifact = self.physical_observation.get("artifact")
        if artifact is not None:
            if not isinstance(artifact, dict):
                errors.append("artifact must be an object when provided")
            else:
                for field in ("path", "sha256"):
                    value = artifact.get(field)
                    if not isinstance(value, str) or not value.strip():
                        errors.append(f"artifact.{field} is required when artifact is provided")
        return errors

    def take_screenshot(self, label: str) -> None:
        path = self.out_dir / f"{label}.png"
        env = os.environ.copy()
        env.update({"XDG_RUNTIME_DIR": "/run/user/1001", "WAYLAND_DISPLAY": self.args.wayland_display})
        result = self.run(["spectacle", "--background", "--nonotify", "--fullscreen", "--output", str(path)], check=False, env=env, timeout=30)
        self.commands[f"screenshot_{label}"] = result.as_dict()
        if result.returncode != 0 or not path.exists():
            raise CanaryError(f"spectacle screenshot failed: {result.stderr or result.stdout}")
        blob = path.read_bytes()
        if not blob.startswith(b"\x89PNG\r\n\x1a\n"):
            raise CanaryError(f"screenshot is not PNG: {path}")
        self.screenshots[label] = {
            "path": str(path),
            "bytes": len(blob),
            "sha256": hashlib.sha256(blob).hexdigest(),
        }


    def generate_composite_evidence(self, data: dict[str, Any], label: str) -> None:
        from PIL import Image, ImageDraw

        canvas = Image.new("RGB", (2560, 1440), "#1b1f2a")
        draw = ImageDraw.Draw(canvas)
        draw.rectangle([0, 0, 2559, 1439], outline="#475569", width=3)
        draw.text((24, 24), "Generated split-shell composite evidence from compositor readback + surface captures", fill="#e5e7eb")

        # Draw layer-shell background/dock first; WebKit layer-shell captures may be
        # rejected by the bridge, so use readback geometry when pixels are absent.
        for s in self.surfaces(data):
            geom = s.get("geometry") or {}
            layer = s.get("layer_shell") or {}
            if s.get("surface_kind") == "layer_shell" and geom.get("height", 0) >= 1000 and layer.get("exclusive_zone") is False:
                draw.rectangle([0, 0, 2559, 1439], fill="#243047")
                draw.text((28, 54), f"split background {s.get('id')} layer={layer.get('layer')} frame_count={s.get('frame_count')}", fill="#dbeafe")
            if s.get("surface_kind") == "layer_shell" and 0 < geom.get("height", 9999) <= 160 and layer.get("exclusive_zone") is True:
                h = int(geom.get("height", 96))
                anchors = set(layer.get("anchors") or [])
                top = 1440 - h if "bottom" in anchors else 0
                draw.rectangle([0, top, 2559, top + h], fill="#111827", outline="#38bdf8", width=3)
                draw.text((28, top + 16), f"split dock {s.get('id')} layer={layer.get('layer')} anchors={sorted(anchors)} exclusive_zone={layer.get('exclusive_zone')}", fill="#e0f2fe")

        for label_name, capture in self.surface_captures.items():
            if not label_name.startswith("app_") or not capture.get("ok"):
                continue
            resp = capture.get("response") or {}
            path = resp.get("path") or resp.get("image_path")
            surface_id = resp.get("surface_id")
            geom = None
            for s in self.surfaces(data):
                if s.get("id") == surface_id:
                    geom = s.get("geometry") or {}
                    break
            if not path or not geom:
                continue
            img = Image.open(path).convert("RGB")
            x, y = int(geom.get("x", 0)), int(geom.get("y", 0))
            canvas.paste(img, (x, y))
            draw.rectangle([x, y, x + img.width, y + img.height], outline="#facc15", width=3)
            draw.text((x + 8, y + 8), f"{label_name} {surface_id}", fill="#fef3c7")

        path = self.out_dir / f"{label}.png"
        canvas.save(path)
        blob = path.read_bytes()
        self.screenshots[label] = {
            "path": str(path),
            "bytes": len(blob),
            "sha256": hashlib.sha256(blob).hexdigest(),
            "kind": "generated_composite_from_readback_and_surface_captures",
        }

    def terminate_matching_processes(self, label: str) -> None:
        result = self.run([self.args.compositorctl, "--pretty", "list-processes"], check=False, timeout=30)
        self.commands[f"list_processes_{label}"] = result.as_dict()
        if result.returncode != 0:
            return
        try:
            data = json.loads(result.stdout)
        except json.JSONDecodeError:
            return
        markers = [
            "io.agoraos.ShellBackground",
            "io.agoraos.ShellDock",
            "AGORA-SHELL-BACKGROUND",
            "AGORA-SHELL-DOCK",
            "io.agoraos.SplitCanaryOne",
            "io.agoraos.SplitCanaryTwo",
        ]
        for proc in data.get("processes", []):
            if proc.get("status") != "running":
                continue
            command = proc.get("command") or ""
            launch_id = proc.get("launch_id")
            if isinstance(launch_id, str) and any(marker in command for marker in markers):
                term = self.run([self.args.compositorctl, "terminate", "--launch-id", launch_id], check=False, timeout=15)
                self.commands[f"terminate_matching_{label}_{launch_id}"] = term.as_dict()

    def terminate_launches(self) -> None:
        for name, launch in list(self.launches.items()):
            launch_id = launch.get("launch_id")
            if launch_id:
                res = self.run([self.args.compositorctl, "terminate", "--launch-id", launch_id], check=False, timeout=15)
                self.commands[f"terminate_{name}"] = res.as_dict()

    def stop_split_supervisor(self) -> None:
        if self.supervisor and self.supervisor.poll() is None:
            self.supervisor.terminate()
            try:
                self.supervisor.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.supervisor.kill()
                self.supervisor.wait(timeout=5)
            self.commands["split_supervisor_stop"] = {"returncode": self.supervisor.returncode}

    def validate_visibility(self, data: dict[str, Any], app_ids: list[str]) -> dict[str, Any]:
        surfaces = []
        for app_id in app_ids:
            item = self.find_by_app_id(data, app_id)
            if not item:
                raise CanaryError(f"missing app surface {app_id}")
            s = item.get("surface") or {}
            if s.get("visible") is not True or s.get("role") != "toplevel" or s.get("surface_kind") != "xdg_view":
                raise CanaryError(f"app {app_id} not visible ordinary xdg toplevel: {s}")
            if s.get("fullscreen") is True:
                raise CanaryError(f"app {app_id} unexpectedly fullscreen")
            surfaces.append({
                "id": s.get("id"),
                "app_id": s.get("app_id"),
                "title": s.get("title"),
                "visible": s.get("visible"),
                "focused": s.get("focused"),
                "role": s.get("role"),
                "surface_kind": s.get("surface_kind"),
                "geometry": s.get("geometry"),
                "stack_layer": s.get("stack_layer"),
                "stack_index": s.get("stack_index"),
                "is_top_in_stack": s.get("is_top_in_stack"),
            })
        shell_surfaces = []
        fullscreen_shells = []
        mapped = set(self.split_surface_ids_from_log().values())
        for s in self.surfaces(data):
            geom = s.get("geometry") or {}
            layer = s.get("layer_shell") or {}
            looks_like_split = (
                s.get("id") in mapped
                or (s.get("surface_kind") == "layer_shell" and s.get("visible") is True and (
                    (geom.get("height", 0) >= 1000 and layer.get("exclusive_zone") is False)
                    or (0 < geom.get("height", 0) <= 160 and layer.get("exclusive_zone") is True)
                ))
            )
            if looks_like_split:
                shell_surfaces.append({
                    "id": s.get("id"),
                    "app_id": s.get("app_id"),
                    "title": s.get("title"),
                    "role": s.get("role"),
                    "surface_kind": s.get("surface_kind"),
                    "layer_shell": s.get("layer_shell"),
                    "geometry": s.get("geometry"),
                    "visible": s.get("visible"),
                    "frame_count": s.get("frame_count"),
                    "capture_count": s.get("capture_count"),
                })
            if s.get("app_id") == "io.agoraos.ShellPanel" and s.get("role") == "toplevel" and s.get("fullscreen") and s.get("visible") is True:
                fullscreen_shells.append(s)
        if fullscreen_shells:
            raise CanaryError(f"fullscreen fallback shell still present during split canary: {fullscreen_shells}")
        has_background = any(((s.get("geometry") or {}).get("height", 0) >= 1000) for s in shell_surfaces)
        has_dock = any((0 < ((s.get("geometry") or {}).get("height", 9999)) <= 160) for s in shell_surfaces)
        if not (has_background and has_dock):
            raise CanaryError(f"split shell background+dock not both present: {shell_surfaces}")
        return {"app_surfaces": surfaces, "split_shell_surfaces": shell_surfaces}

    def final_packet(self, status: str, extra: dict[str, Any] | None = None) -> dict[str, Any]:
        packet = {
            "task_id": self.args.task_id,
            "status": status,
            "started_at": self.started_at,
            "finished_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "output_dir": str(self.out_dir),
            "commands": self.commands,
            "launches": self.launches,
            "surface_captures": self.surface_captures,
            "screenshots": self.screenshots,
            "failures": self.failures,
        }
        if extra:
            packet.update(extra)
        packet_path = self.out_dir / "evidence-packet.json"
        packet_path.write_text(json.dumps(packet, indent=2, sort_keys=True), encoding="utf-8")
        return packet

    def run_canary(self) -> dict[str, Any]:
        app1_id = "io.agoraos.SplitCanaryOne"
        app2_id = "io.agoraos.SplitCanaryTwo"
        validation: dict[str, Any] = {}
        try:
            self.commands["service_state_before"] = self.shell("systemctl is-active event-bus.service event-bus-web.service compositor-bridge.service agora-shell-panel.service agora-wayfire.service", check=False).as_dict()
            self.terminate_matching_processes("preflight")
            self.list_surfaces("before")
            self.stop_panel_service()
            self.list_surfaces("after-panel-stop")

            app1 = self.launch_app("app_one", app1_id, "AGORA-SPLIT-CANARY-ONE")
            app2 = self.launch_app("app_two", app2_id, "AGORA-SPLIT-CANARY-TWO")
            app1_surface = app1["surface"]["surface"]["id"]
            app2_surface = app2["surface"]["surface"]["id"]
            self.tile_surface("app_one", app1_surface, 0, 0)
            self.tile_surface("app_two", app2_surface, 0, 1)

            self.start_split_supervisor()
            self.wait_for_split_surfaces()
            after_split = self.list_surfaces("after-split-launch")
            validation = self.validate_visibility(after_split, [app1_id, app2_id])

            action = self.focus_and_raise("app_one", app1_surface)
            after_action = self.list_surfaces("after-action")
            validation["action"] = action
            validation["after_action"] = self.validate_visibility(after_action, [app1_id, app2_id])

            self.capture_surface("app_one", app1_surface, required=True)
            self.capture_surface("app_two", app2_surface, required=True)
            for item in after_action.get("surfaces", []):
                s = item.get("surface") or {}
                surface_id = s.get("id")
                if not isinstance(surface_id, str) or not surface_id:
                    continue
                geom = s.get("geometry") or {}
                layer = s.get("layer_shell") or {}
                if s.get("surface_kind") == "layer_shell" and geom.get("height", 0) >= 1000 and layer.get("exclusive_zone") is False:
                    self.capture_surface("background", surface_id, required=False)
                if s.get("surface_kind") == "layer_shell" and 0 < geom.get("height", 9999) <= 160 and layer.get("exclusive_zone") is True:
                    self.capture_surface("dock", surface_id, required=False)
            validation["dock_presentation"] = self.evaluate_dock_presentation(after_action)
            if self.args.require_dock_presentation_evidence and validation["dock_presentation"].get("verdict") != "presented":
                if validation["dock_presentation"].get("verdict") == "missing_dock_surface":
                    raise CanaryError("split dock presentation gate failed: no dock layer-shell surface found")
                raise CanaryError(
                    "split dock presentation gate failed: dock is mapped, but has no frame_count, "
                    "last_present_timestamp, successful dock capture, or explicit physical observation"
                )

            try:
                self.take_screenshot("split-shell-physical-spectacle")
            except Exception as exc:
                self.screenshots["split-shell-physical-spectacle"] = {"ok": False, "error": str(exc)}
            self.generate_composite_evidence(after_action, "split-shell-generated-composite")

            self.stop_split_supervisor()
            self.terminate_launches()
            self.terminate_matching_processes("post-supervisor-stop")
            time.sleep(1.0)
            cleanup_data = self.list_surfaces("after-cleanup")
            leftovers = []
            for s in self.surfaces(cleanup_data):
                geom = s.get("geometry") or {}
                layer = s.get("layer_shell") or {}
                split_leftover = s.get("surface_kind") == "layer_shell" and (
                    (geom.get("height", 0) >= 1000 and layer.get("exclusive_zone") is False)
                    or (0 < geom.get("height", 9999) <= 160 and layer.get("exclusive_zone") is True)
                )
                if s.get("app_id") in {app1_id, app2_id, *SHELL_APP_IDS} or split_leftover:
                    leftovers.append({"id": s.get("id"), "app_id": s.get("app_id"), "title": s.get("title"), "geometry": s.get("geometry")})
            if leftovers:
                raise CanaryError(f"canary leftovers after cleanup: {leftovers}")
            validation["cleanup_leftovers"] = leftovers
            return self.final_packet("passed", {"validation": validation})
        except Exception as exc:
            self.failures.append(str(exc))
            try:
                self.stop_split_supervisor()
            finally:
                self.terminate_launches()
                self.terminate_matching_processes("exception")
            return self.final_packet("failed", {"validation": validation})
        finally:
            self.terminate_matching_processes("finally")
            self.restore_panel_service()
            # Preserve post-restore readback in a separate file as the evidence packet
            # may already be written in the exception path.
            try:
                restore_state = self.list_surfaces("post-restore")
                (self.out_dir / "post-restore-summary.json").write_text(json.dumps({
                    "panel_was_active": self.panel_was_active,
                    "panel_stopped": self.panel_stopped,
                    "shell_surfaces": [
                        {"id": s.get("id"), "title": s.get("title"), "app_id": s.get("app_id"), "role": s.get("role"), "surface_kind": s.get("surface_kind")}
                        for s in self.surfaces(restore_state)
                        if "Shell" in (s.get("app_id") or "") or "Agora Desktop Shell" in (s.get("title") or "") or "AGORA-SHELL" in (s.get("title") or "")
                    ],
                }, indent=2), encoding="utf-8")
            except Exception as exc:
                self.failures.append(f"post-restore readback failed: {exc}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run the split-shell live evidence canary.")
    parser.add_argument("--output-dir", default="", help="Evidence output directory (default: /tmp/agora-split-shell-canary/<timestamp>)")
    parser.add_argument("--task-id", type=int, default=3495, help="Task id to record in the evidence packet")
    parser.add_argument("--compositorctl", default="compositorctl")
    parser.add_argument("--supervisor", default="", help="Supervisor path (default: /usr/local/bin/agora-shell-panel-supervisor)")
    parser.add_argument("--wait-timeout-ms", type=int, default=12000)
    parser.add_argument("--wayland-display", default="wayland-1")
    parser.add_argument("--require-dock-presentation-evidence", action=argparse.BooleanOptionalAction, default=True, help="Fail unless the dock has compositor/capture/manual physical presentation evidence, not only mapped readback")
    parser.add_argument("--physical-observation-file", default="", help="Optional JSON receipt with dock_visible=true, output/output_id, observed_by, observed_at, and optional dock_surface_id/artifact")
    parser.add_argument("--manage-panel-service", action=argparse.BooleanOptionalAction, default=True, help="Stop/restore agora-shell-panel.service around the canary")
    parser.add_argument("--restore-panel-service", action=argparse.BooleanOptionalAction, default=True)
    return parser.parse_args()


def main() -> int:
    canary = Canary(parse_args())
    packet = canary.run_canary()
    print(json.dumps({
        "status": packet["status"],
        "output_dir": packet["output_dir"],
        "evidence_packet": str(pathlib.Path(packet["output_dir"]) / "evidence-packet.json"),
        "screenshots": packet.get("screenshots", {}),
        "failures": packet.get("failures", []),
    }, indent=2))
    return 0 if packet["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
