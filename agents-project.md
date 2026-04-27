# Agora OS Local Bootstrap

Project-specific live guidance lives in Den at `[doc: agora-os/project-bootstrap-guide]`.

Use project ID `agora-os` for Den tasks, messages, documents, librarian queries, and guidance lookups.

## Local Commands

```sh
go build ./cmd/...
```

Do not run system services on the host. They create system users, modify nftables, and write to `/var/log/agent-os/`. Use the disposable VM workflow for privileged validation.
