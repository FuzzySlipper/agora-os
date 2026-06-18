project(
  'agora-wayfire-plugin',
  'cpp',
  version: '0.1.0',
  default_options: [
    'cpp_std=c++17',
    'warning_level=2',
    'werror=false',
  ],
)

wayfire = dependency('wayfire')
wayland_server = dependency('wayland-server')
wfconfig = dependency('wf-config', required: false)
threads = dependency('threads')
wayland_scanner = find_program('wayland-scanner')

layer_shell_server_header = custom_target(
  'wlr-layer-shell-server-header',
  input: 'protocols/wlr-layer-shell-unstable-v1.xml',
  output: 'wlr-layer-shell-unstable-v1-protocol.h',
  command: [wayland_scanner, 'server-header', '@INPUT@', '@OUTPUT@'],
)

wayfire_plugin_dir = wayfire.get_variable(
  pkgconfig: 'plugindir',
  default_value: join_paths(get_option('libdir'), 'wayfire'),
)
wayfire_metadata_dir = wayfire.get_variable(
  pkgconfig: 'metadatadir',
  default_value: join_paths(get_option('datadir'), 'wayfire', 'metadata'),
)

plugin_deps = [wayfire, wayland_server, threads]
if wfconfig.found()
  plugin_deps += wfconfig
endif

shared_module(
  'agora-bridge',
  ['src/plugin.cpp', layer_shell_server_header],
  include_directories: include_directories('src'),
  dependencies: plugin_deps,
  install: true,
  install_dir: wayfire_plugin_dir,
)

protocol_smoke = executable(
  'protocol-smoke',
  ['tests/protocol_smoke.cpp', layer_shell_server_header],
  include_directories: include_directories('src'),
  dependencies: plugin_deps,
)

test('protocol-smoke', protocol_smoke)

install_data(
  'metadata/agora-bridge.xml',
  install_dir: wayfire_metadata_dir,
)
