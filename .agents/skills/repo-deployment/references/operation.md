# pemcast operation

## Configure

```bash
pemcast config example > /etc/pemcast/config.yaml
pemcast --config /etc/pemcast/config.yaml config validate
```

Effective values are defaults, then the first default file, explicit config, `PEMCAST_*` environment values, then CLI flags. Environment keys use full schema paths with `.` and `-` changed to `_`:

```text
PEMCAST_AGENT_ETCD_ENDPOINTS='["https://etcd-1:2379"]'
PEMCAST_AGENT_ONCE=true
PEMCAST_AGENT_WATCH_RESYNC_INTERVAL=10m
PEMCAST_AGENT_TARGETS='[{"id":"nginx",...}]'
```

Scalars and durations are strings. Structs, slices, and maps are JSON documents.

Configure etcd access, one target per consumer, output root, safe mappings, validation pair, and hook. The agent user needs persistent write access to state and output paths. Run exactly one process per output root or target set.

## Validate and start

```bash
pemcast --config /etc/pemcast/config.yaml agent --once --dry-run
pemcast --config /etc/pemcast/config.yaml agent --once
pemcast --config /etc/pemcast/config.yaml agent
```

Use a supervisor with restart on failure and `SIGTERM`/`SIGINT` for shutdown. Keep state and output paths persistent unless first-boot synchronization is intended.

Container example:

```bash
docker run --rm \
  -v /etc/pemcast/config.yaml:/app/data/config/config.yaml:ro \
  -v /etc/nginx/tls:/etc/nginx/tls \
  -v /var/lib/pemcast:/var/lib/pemcast \
  ghcr.io/lwmacct/260907-pemcast:<tag> \
  pemcast --config /app/data/config/config.yaml agent
```

If a hook must affect the host, use a host-level agent or explicitly mount and authorize a hook that works across the container boundary.

## Hook contract

The executable is direct, shell-free, and receives:

```text
PEMCAST_TARGET
PEMCAST_GENERATION
PEMCAST_PREVIOUS_GENERATION
PEMCAST_ETCD_REVISION
PEMCAST_RELEASE_DIR
PEMCAST_CURRENT_DIR
PEMCAST_CHANGED_FILES
PEMCAST_BUNDLE_SHA256
```

JSON on stdin uses fields `target-id`, `generation`, `previous-generation`, `etcd-revision`, `release-dir`, `current-dir`, `changed-files`, `bundle-sha256`, and `activated-at`. Hooks run after activation and must be idempotent.

## Diagnose

```bash
readlink -f /etc/nginx/tls/current
cat /etc/nginx/tls/current/.pemcast-digest
stat -c '%a %U:%G %n' /etc/nginx/tls/current/privkey.pem
cat /var/lib/pemcast/nginx.json | jq .
```

Classify failures:

- etcd connect/read/watch: endpoint, auth, TLS, or compaction;
- fetch/hash/manifest/TLS validity: malformed or partial remote bundle;
- deploy: output permissions or filesystem failure;
- hook timeout/nonzero: executable, authorization, or downstream failure.

Roll back by moving the active pointer to a complete old generation. pemcast recreates a pruned local release when needed. Deleting the pointer never deletes local files: `retain` keeps serving it, `fail` reports the deletion.

For manual recovery, stop the sole agent first, repair a complete release and symlink, then restart and dry-run the intended generation. Do not edit `.pemcast` internals during normal operation.
