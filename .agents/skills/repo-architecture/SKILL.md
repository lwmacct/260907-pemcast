---
name: repo-architecture
description: "Explain, review, or change pemcast architecture, package boundaries, synchronization invariants, or failure behavior. Not for routine deployment operations or test-only edits."
---

# pemcast architecture

Use this skill when the task requires understanding why pemcast is designed this way or changing behavior that crosses package boundaries.

1. Read [references/architecture.md](references/architecture.md) before explaining or modifying the design.
2. Treat the etcd protocol and local activation invariants as contracts. If a requested change weakens one, state the consequence and propose a safer design.
3. Verify architecture changes with focused package tests plus `go test ./...`. If `internal/reconcile` changes materially, add controller-level tests because that package currently has no direct tests.

Current operational constraint: there is no cross-process lock in the state store. Do not recommend running more than one agent process against the same output root or target set.
