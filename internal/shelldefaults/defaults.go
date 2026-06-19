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
