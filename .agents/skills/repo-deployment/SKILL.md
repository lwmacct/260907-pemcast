---
name: repo-deployment
description: "Configure and operate pemcast, publish or roll back etcd certificate bundles, run dry-run checks, and diagnose activation or reload-hook failures. Not for internal implementation changes."
---

# pemcast deployment

Use this skill for operator tasks. Select only the references needed by the request:

- For agent configuration, startup, container wiring, failure behavior, rollback, or cleanup, read [references/operation.md](references/operation.md).
- For creating, publishing, verifying, or rolling back a certificate generation in etcd, read [references/publishing.md](references/publishing.md).
- For a live deployment or etcd mutation, first confirm the exact target environment and requested change. Prefer `agent --once --dry-run` as the non-mutating validation step.

Never move the active pointer until the complete immutable generation has been written and verified. Keep one agent process reconciling each output root or target set.
