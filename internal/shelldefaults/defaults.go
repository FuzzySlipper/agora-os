package shelldefaults

const LayoutJSON = `{
  "widgets": {
    "clock": { "visible": true, "position": "top-right", "order": 1 },
    "hello-world": { "visible": true, "position": "top-left", "order": 1 },
    "notifications": { "visible": true, "position": "bottom-right", "order": 1 },
    "taskbar": { "visible": true, "position": "bottom", "order": 1 },
    "center": { "visible": false, "position": "center", "order": 1 }
  }
}
`

const HelloWorldIndexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Hello World</title>
<style>body{font-family:sans-serif;color:#e2e8f0;padding:0.8rem;margin:0;background:transparent}
h2{margin:0 0 0.3rem;font-size:1rem}.ts{font-size:0.7rem;color:#94a3b8;opacity:0.8}</style>
</head><body>
<h2>👋 Hello World</h2>
<div class="ts" id="ts"></div>
<script>document.getElementById('ts').textContent=new Date().toLocaleTimeString();
window.parent.postMessage({type:"pub",topic:"hello-world.loaded",body:{ts:Date.now()}},"*");</script>
</body></html>
`

const HelloWorldManifestJSON = `{
  "name": "hello-world",
  "title": "Hello World",
  "position": "top-left",
  "size": { "width": 180, "height": 60 },
  "bus_topics": []
}
`

const HelloWorldWidgetName = "hello-world"
const DefaultThemeID = "agora-default"

const DefaultThemeManifestJSON = `{
  "schema": "agora-desktop-shell-theme/v0.1",
  "id": "agora-default",
  "name": "Agora Default",
  "description": "Bundled Agora Desktop Shell default theme package.",
  "version": "0.1.0",
  "author": "agora-os",
  "extends": null,
  "metadata": {
    "status": "bundled",
    "source_doc": "agora-os/agora-desktop-shell-theme-customization-contract"
  },
  "tokens": {
    "semantic.color.accent.primary": "#76e4f7",
    "semantic.color.text.primary": "#edf3ff",
    "semantic.color.text.secondary": "#94a3b8",
    "component.taskbar.background": "rgba(10, 14, 24, 0.88)",
    "component.command_center.panel.background": "linear-gradient(145deg, rgba(15, 23, 42, 0.96), rgba(31, 41, 55, 0.94))",
    "state.focus.ring": "0 0 0 2px rgba(118, 228, 247, 0.20)",
    "motion.duration.fast": "120ms"
  },
  "assets": {
    "wallpaper": {
      "kind": "css-gradient",
      "value": "radial-gradient(circle at 18% 12%, rgba(118, 228, 247, 0.20), transparent 34%), radial-gradient(circle at 82% 28%, rgba(129, 140, 248, 0.18), transparent 30%), linear-gradient(135deg, #05070d 0%, #080b12 52%, #0b1220 100%)"
    }
  },
  "overrides": {},
  "visual_contracts": {}
}
`
