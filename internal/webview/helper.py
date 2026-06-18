#!/usr/bin/env python3
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import sys
import threading

import gi

gi.require_version("Gtk", "3.0")
try:
    gi.require_version("WebKit2", "4.1")
except ValueError:
    gi.require_version("WebKit2", "4.0")
try:
    gi.require_version("GtkLayerShell", "0.1")
    from gi.repository import GtkLayerShell
except (ImportError, ValueError):
    GtkLayerShell = None

from gi.repository import Gio, GLib, Gtk, WebKit2

LAYER_ROLE_CONFIGS = {
    "panel": ("TOP", ("TOP",), True),
    "dock": ("TOP", ("LEFT",), True),
    "background": ("BOTTOM", ("TOP", "BOTTOM", "LEFT", "RIGHT"), False),
    "overlay": ("OVERLAY", ("BOTTOM", "RIGHT"), False),
}
LAYER_NAMESPACE = "agora-webview"


def emit(event, window, role, layer_shell_info=None):
    payload = {
        "event": event,
        "title": window.get_title() or "",
        "pid": os.getpid(),
        "role": role,
        "surface_kind": "xdg_view" if role == "toplevel" else "layer_shell",
    }
    if layer_shell_info:
        payload.update(layer_shell_info)
    print(json.dumps(payload), flush=True)


READINESS_POLL_SCRIPT = r"""
(function () {
  function collectAgoraReadiness() {
    const stateText = document.getElementById('state')?.textContent || '';
    let parsedState = null;
    try { parsedState = stateText ? JSON.parse(stateText) : null; } catch (_err) { parsedState = null; }
    const markers = {
      title: document.title || '',
      bodyReady: document.body?.dataset?.ready || '',
      bodyStep: document.body?.dataset?.step || '',
      bodyProjectionHash: document.body?.dataset?.projectionHash || '',
      scenarioId: document.querySelector('main[data-scenario]')?.dataset?.scenario || window.ashaAgoraControl?.scenarioId || parsedState?.scenarioId || '',
      step: parsedState?.step ?? (document.body?.dataset?.step ? Number(document.body.dataset.step) : null),
      projectionHash: parsedState?.projectionHash || document.body?.dataset?.projectionHash || '',
      stateText,
    };
    window.webkit.messageHandlers.agoraReadiness.postMessage(JSON.stringify(markers));
  }
  window.addEventListener('load', collectAgoraReadiness);
  window.addEventListener('keydown', () => setTimeout(collectAgoraReadiness, 0), true);
  window.addEventListener('click', () => setTimeout(collectAgoraReadiness, 0), true);
  setInterval(collectAgoraReadiness, 250);
  setTimeout(collectAgoraReadiness, 0);
})();
"""


class Launcher(Gtk.Application):
    def __init__(self, uri, title, width, height, app_id, role, app_command_port):
        super().__init__(application_id=app_id, flags=Gio.ApplicationFlags.FLAGS_NONE)
        self.uri = uri
        self.initial_title = title
        self.width = width
        self.height = height
        self.role = role
        self.app_command_port = app_command_port
        self.effective_role = role
        self.window = None
        self.webview = None
        self.created_emitted = False
        self.layer_shell_info = None
        self.readiness_state = {}
        self.readiness_lock = threading.Lock()
        self.command_server = None

    def do_activate(self):
        if self.window is not None:
            self.window.present()
            return

        self.window = Gtk.ApplicationWindow(application=self)
        self.window.set_default_size(self.width, self.height)
        self.window.set_title(self.initial_title or self.uri)
        self.window.connect("destroy", self.on_destroy)
        self.window.connect("notify::is-active", self.on_is_active)
        self.setup_layer_shell()

        content_manager = WebKit2.UserContentManager()
        content_manager.register_script_message_handler("agoraReadiness")
        content_manager.connect("script-message-received::agoraReadiness", self.on_readiness_message)
        content_manager.add_script(WebKit2.UserScript(
            READINESS_POLL_SCRIPT,
            WebKit2.UserContentInjectedFrames.TOP_FRAME,
            WebKit2.UserScriptInjectionTime.END,
            None,
            None,
        ))

        self.webview = WebKit2.WebView.new_with_user_content_manager(content_manager)
        self.webview.connect("notify::title", self.on_title_changed)
        self.window.add(self.webview)
        self.webview.load_uri(self.uri)

        self.start_command_server()

        self.window.show_all()
        GLib.idle_add(self.emit_created)

    def setup_layer_shell(self):
        if self.role == "toplevel":
            return
        if GtkLayerShell is None or not GtkLayerShell.is_supported():
            print("GtkLayerShell unsupported; falling back to toplevel", file=sys.stderr, flush=True)
            self.effective_role = "toplevel"
            return
        layer_name, edge_names, exclusive = LAYER_ROLE_CONFIGS[self.role]
        GtkLayerShell.init_for_window(self.window)
        GtkLayerShell.set_namespace(self.window, LAYER_NAMESPACE)
        GtkLayerShell.set_layer(self.window, getattr(GtkLayerShell.Layer, layer_name))
        for edge_name in edge_names:
            GtkLayerShell.set_anchor(self.window, getattr(GtkLayerShell.Edge, edge_name), True)
        if exclusive:
            GtkLayerShell.auto_exclusive_zone_enable(self.window)
        self.layer_shell_info = {
            "namespace": LAYER_NAMESPACE,
            "layer": layer_name.lower(),
            "anchors": [edge_name.lower() for edge_name in edge_names],
            "exclusive_zone": exclusive,
        }

    def ensure_created(self):
        if self.window is None or self.created_emitted:
            return
        emit("created", self.window, self.effective_role, self.layer_shell_info)
        self.created_emitted = True

    def emit_created(self):
        self.ensure_created()
        return False

    def on_destroy(self, *_args):
        if self.command_server is not None:
            self.command_server.shutdown()
            self.command_server.server_close()
        self.ensure_created()
        emit("closed", self.window, self.effective_role, self.layer_shell_info)
        self.quit()

    def on_is_active(self, *_args):
        if self.window is not None and self.window.is_active():
            self.ensure_created()
            emit("focused", self.window, self.effective_role, self.layer_shell_info)

    def on_title_changed(self, webview, _param):
        title = webview.get_title()
        if title:
            self.window.set_title(title)

    def on_readiness_message(self, _manager, js_result):
        try:
            value = js_result.get_js_value()
            raw = value.to_string()
            parsed = json.loads(raw)
            if isinstance(parsed, dict):
                with self.readiness_lock:
                    self.readiness_state = parsed
        except Exception as exc:
            print(f"readiness message ignored: {exc}", file=sys.stderr, flush=True)

    def start_command_server(self):
        if self.app_command_port <= 0:
            return
        launcher = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *_args):
                return

            def do_POST(self):
                if self.path != "/command":
                    self.send_error(404)
                    return
                try:
                    length = int(self.headers.get("content-length", "0"))
                    envelope = json.loads(self.rfile.read(length) or b"{}")
                    command = envelope.get("command") if isinstance(envelope, dict) else None
                    if not isinstance(command, dict) or command.get("type") not in ("readiness", "read_proof_markers"):
                        raise ValueError("supported command types: readiness, read_proof_markers")
                    with launcher.readiness_lock:
                        state = dict(launcher.readiness_state)
                    payload = {"ok": True, "readiness": state}
                    raw = json.dumps(payload).encode("utf-8")
                    self.send_response(200)
                    self.send_header("content-type", "application/json")
                    self.send_header("content-length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                except Exception as exc:
                    raw = json.dumps({"ok": False, "error": str(exc)}).encode("utf-8")
                    self.send_response(400)
                    self.send_header("content-type", "application/json")
                    self.send_header("content-length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)

        self.command_server = ThreadingHTTPServer(("127.0.0.1", self.app_command_port), Handler)
        thread = threading.Thread(target=self.command_server.serve_forever, daemon=True)
        thread.start()


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Agora OS WebKitGTK helper")
    parser.add_argument("--uri", required=True)
    parser.add_argument("--title", default="")
    parser.add_argument("--app-id", required=True)
    parser.add_argument("--width", type=int, default=1280)
    parser.add_argument("--height", type=int, default=800)
    parser.add_argument("--role", choices=("toplevel", "panel", "dock", "background", "overlay"), default="toplevel")
    parser.add_argument("--app-command-port", type=int, default=0)
    return parser.parse_args(argv)


def main(argv):
    args = parse_args(argv)
    app = Launcher(args.uri, args.title, args.width, args.height, args.app_id, args.role, args.app_command_port)
    return app.run([])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
