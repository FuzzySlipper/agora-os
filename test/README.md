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

## Integration tests

- **`test/phase1.sh`**: end-to-end Phase 1 scenario. Starts all three services
  (isolation, admin-agent, audit), spawns an agent with resource limits
  (cpu 50%, mem 256M, net deny), then verifies:
  - cgroup limits are enforced (cpu.max, memory.max in cgroupfs)
  - outbound network access is blocked by nftables
  - file I/O is captured in the audit log attributed to the agent uid
  - escalation request is logged with kernel-verified uid and safe default decision
  - terminate removes the user, systemd unit, slice, nft rules, and home directory

  Runs 19 assertions. Requires root, `python3`, and `nft`.

- **`test/phase1-peercred.sh`**: focused proof that `SO_PEERCRED` attribution
  overrides self-reported identity and that cross-uid authorization checks
  hold. Creates a temporary user, starts isolation + admin-agent, and runs
  four assertions (uid override in admin-agent log, spawn denied for non-root,
  cross-uid terminate denied, list_agents filtered). Requires root and
  `python3`.

## Typical loop

```sh
scripts/vm.sh start
scripts/vm.sh ssh -- 'cd /repo && go test ./...'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1.sh'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1-peercred.sh'
scripts/vm.sh stop
```
