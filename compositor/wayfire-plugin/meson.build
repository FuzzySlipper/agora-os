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

plugin_deps = [wayfire, wayland_server, threads]
if wfconfig.found()
  plugin_deps += wfconfig
endif

shared_module(
  'agora-bridge',
  ['src/plugin.cpp'],
  include_directories: include_directories('src'),
  dependencies: plugin_deps,
  install: true,
  install_dir: join_paths(get_option('libdir'), 'wayfire'),
)

install_data(
  'metadata/agora-bridge.xml',
  install_dir: join_paths(get_option('datadir'), 'wayfire', 'metadata'),
)
