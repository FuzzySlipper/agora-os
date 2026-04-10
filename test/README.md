# Test Guide

Phase 1 testing is **VM-first**. Changes that depend on kernel attribution,
cross-uid behavior, privileged paths, or host services are not considered
validated based on local sandbox runs alone.

## Use the VM for authoritative checks

- Validate `SO_PEERCRED` behavior in the disposable VM.
- Validate Unix sockets under `/run/agent-os` in the VM.
- Validate `useradd`, per-uid process execution, and `systemd-run` in the VM.
- Validate nftables, fanotify, and writes under `/var/log/agent-os` in the VM.
- Prefer reproducible integration scripts under `test/` for Phase 1 acceptance.

The local sandbox may block socket creation or other privileged operations, so
`go test ./...` on the host can produce false negatives for Phase 1 system
behavior.

## Current scaffolds

- `test/phase1-peercred.sh`: integration outline for proving that
  `SO_PEERCRED` attribution overrides self-reported identity and that
  cross-uid authorization checks hold in the VM.

## Typical loop

```sh
scripts/vm.sh start
scripts/vm.sh ssh -- 'cd /repo && go test ./...'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1-peercred.sh'
scripts/vm.sh stop
```
