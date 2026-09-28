# pemcast architecture

## Positioning

pemcast is a pull-only certificate agent. A publisher stores complete immutable certificate generations in etcd and moves an active pointer only after a generation is ready. Each process reads pointers for configured targets, validates material, materializes a local immutable release, and switches one stable symlink. Reload is delegated to a trusted hook.

There is no bundled publisher, lease manager, service-control integration, or HTTP API.

## Runtime flow

Startup validates configuration, creates the state directory, constructs the etcd client, and assembles the reconcile controller. State creation currently does not acquire a cross-process lock.

`agent --once` takes one active-pointer snapshot and reconciles every configured target.

Normal agent mode loops:

1. Read all active pointers and record the etcd revision.
2. Reconcile the snapshot concurrently.
3. Watch active pointers from `snapshot.Revision + 1`.
4. Handle relevant puts and deletes.
5. Restart from a fresh snapshot at each `resync-interval`.
6. Retry snapshot/watch failures with bounded exponential delay and jitter.

Individual reconcile failures in watch mode are logged and do not stop the process.

## Remote model

```text
<root>/active/<target-id> = <generation>
<root>/bundles/<target-id>/<generation>/manifest.json
<root>/bundles/<target-id>/<generation>/files/<file-name>
```

Target IDs, generations, and file names are one safe path component, at most 128 bytes, and use alphanumerics or a non-leading `.`, `_`, or `-`. Bundles are fetched at the snapshot/event revision. Unknown keys, missing manifests, unsafe names, and files over 4 MiB are rejected.

Manifest decoding rejects unknown members. It requires schema `pemcast/v1`, unique safe file names with lowercase SHA-256 values, and pairs referencing declared files. The fetched file set must exactly equal the declared set. `kind` is not semantically validated.

The whole-bundle digest hashes raw per-file SHA-256 values in manifest-name order, with each name and a NUL byte before its digest. Local release names use that digest.

## Reconciliation

For each target:

1. Serialize the target with its in-process mutex.
2. Acquire a global concurrency slot.
3. Fetch the bundle at the event revision.
4. Validate the configured certificate/key pair, leaf validity, and declared pair.
5. Load state.
6. Stop before writes in dry-run mode.
7. Activate only when the selected local digest differs.
8. Run the hook after activation or to retry a recorded hook failure.
9. Atomically save state.

Different targets can run concurrently. A configured target absent from a snapshot uses `delete-policy`: `retain` keeps local files and logs, while `fail` returns an error. pemcast never deletes local targets automatically.

## Local activation

For `/etc/nginx/tls`:

```text
/etc/nginx/tls/
├── current -> .pemcast/releases/sha256-<digest>
└── .pemcast/releases/
    ├── sha256-old/
    └── sha256-active/
```

A release is staged, populated with configured mappings and modes, marked with `.pemcast-digest`, fsynced, renamed into place, then selected by atomic symlink rename. The active release is never pruned. `retain-releases` controls only additional inactive releases.

Distinct generations with identical content map to one digest and one release.

## Hooks and state

Hooks execute directly without a shell, receive selected environment variables, and get one JSON event on stdin. Timeout is bounded. stdout and stderr are combined and included, truncated to 4096 bytes, on failure.

The hook runs after the symlink switch. Failure records `hook-error`; later events or resyncs retry without rewriting the unchanged release. If the process stops between activation and state save, the hook can run again, so hooks must be idempotent.

State is a small JSON file below `state-dir`, written by temporary file, fsync, rename, and parent-directory fsync.

## Package map

- `cmd/pemcast`: CLI entry and signal context.
- `internal/appcmd/agent`: application assembly and once/watch lifecycle.
- `internal/appcmd/config`: config example and validation commands.
- `internal/config`: schema, defaults, modes, validation, cfgm integration.
- `internal/etcdsource`: etcd client, snapshot, watch, revision-pinned fetch.
- `internal/bundle`: manifest, digests, TLS key pair, validity.
- `internal/reconcile`: orchestration, locks, concurrency, hook retry.
- `internal/deploy`: immutable releases, atomic symlink, pruning, fsync.
- `internal/hook`: trusted direct execution and event schema.
- `internal/state`: durable activation and hook state.

Known boundaries: no cross-process lock, no direct etcdsource/reconcile tests, container supplies only the binary and example config, and publisher immutability is an external contract.
