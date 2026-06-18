#!/usr/bin/env python3
import argparse
import json
import os
import sys

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


def emit(event, window, role):
    payload = {
        "event": event,
        "title": window.get_title() or "",
        "pid": os.getpid(),
        "role": role,
    }
    print(json.dumps(payload), flush=True)


class Launcher(Gtk.Application):
    def __init__(self, uri, title, width, height, app_id, role):
        super().__init__(application_id=app_id, flags=Gio.ApplicationFlags.FLAGS_NONE)
        self.uri = uri
        self.initial_title = title
        self.width = width
        self.height = height
        self.role = role
        self.effective_role = role
        self.window = None
        self.webview = None
        self.created_emitted = False

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

        self.webview = WebKit2.WebView()
        self.webview.connect("notify::title", self.on_title_changed)
        self.window.add(self.webview)
        self.webview.load_uri(self.uri)

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
        GtkLayerShell.set_namespace(self.window, "agora-webview")
        GtkLayerShell.set_layer(self.window, getattr(GtkLayerShell.Layer, layer_name))
        for edge_name in edge_names:
            GtkLayerShell.set_anchor(self.window, getattr(GtkLayerShell.Edge, edge_name), True)
        if exclusive:
            GtkLayerShell.auto_exclusive_zone_enable(self.window)

    def ensure_created(self):
        if self.window is None or self.created_emitted:
            return
        emit("created", self.window, self.effective_role)
        self.created_emitted = True

    def emit_created(self):
        self.ensure_created()
        return False

    def on_destroy(self, *_args):
        self.ensure_created()
        emit("closed", self.window, self.effective_role)
        self.quit()

    def on_is_active(self, *_args):
        if self.window is not None and self.window.is_active():
            self.ensure_created()
            emit("focused", self.window, self.effective_role)

    def on_title_changed(self, webview, _param):
        title = webview.get_title()
        if title:
            self.window.set_title(title)


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Agora OS WebKitGTK helper")
    parser.add_argument("--uri", required=True)
    parser.add_argument("--title", default="")
    parser.add_argument("--app-id", required=True)
    parser.add_argument("--width", type=int, default=1280)
    parser.add_argument("--height", type=int, default=800)
    parser.add_argument("--role", choices=("toplevel", "panel", "dock", "background", "overlay"), default="toplevel")
    return parser.parse_args(argv)


def main(argv):
    args = parse_args(argv)
    app = Launcher(args.uri, args.title, args.width, args.height, args.app_id, args.role)
    return app.run([])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
