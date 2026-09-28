# Publish a pemcast bundle

For target `nginx`, generation `01K4GENERATION`, and root `/pemcast/v1`:

```text
/pemcast/v1/active/nginx = 01K4GENERATION
/pemcast/v1/bundles/nginx/01K4GENERATION/manifest.json
/pemcast/v1/bundles/nginx/01K4GENERATION/files/fullchain.pem
/pemcast/v1/bundles/nginx/01K4GENERATION/files/privkey.pem
```

Write and verify every bundle key before moving the pointer. Generations are immutable; use a new name for any content change.

Prepare the manifest:

```bash
TARGET=nginx
GENERATION=01K4GENERATION
ROOT=/pemcast/v1
CERT_SHA256="$(sha256sum fullchain.pem | awk '{print $1}')"
KEY_SHA256="$(sha256sum privkey.pem | awk '{print $1}')"

cat > manifest.json <<EOF
{
  "schema": "pemcast/v1",
  "files": [
    {"name":"fullchain.pem","kind":"certificate","sha256":"$CERT_SHA256"},
    {"name":"privkey.pem","kind":"private-key","sha256":"$KEY_SHA256"}
  ],
  "pairs": [
    {"certificate":"fullchain.pem","private-key":"privkey.pem"}
  ]
}
EOF
```

With `ETCDCTL_ENDPOINTS` and auth/TLS options set:

```bash
BUNDLE="$ROOT/bundles/$TARGET/$GENERATION"
etcdctl put "$BUNDLE/files/fullchain.pem" -- < fullchain.pem
etcdctl put "$BUNDLE/files/privkey.pem" -- < privkey.pem
etcdctl put "$BUNDLE/manifest.json" -- < manifest.json
etcdctl get "$BUNDLE/" --prefix
```

Confirm exact keys and digests. Each file must be at most 4 MiB, and fetched and declared file sets must match exactly.

Activate only after verification:

```bash
OLD="$(etcdctl get "$ROOT/active/$TARGET" --print-value-only)"
if [ -n "$OLD" ]; then
  etcdctl put "$ROOT/active/$TARGET" "$GENERATION" --prev-value="$OLD"
else
  etcdctl put "$ROOT/active/$TARGET" "$GENERATION"
fi
```

Then dry-run and synchronize:

```bash
pemcast --config /etc/pemcast/config.yaml agent --once --dry-run
pemcast --config /etc/pemcast/config.yaml agent --once
```

Rollback is another pointer move to a complete immutable old generation. Identical content resolves to the same digest and may not rewrite or invoke a hook.

Publisher obligations: never mutate an exposed generation, keep old generations for rollback, hash exact bytes, store credentials securely, and move the pointer last.
