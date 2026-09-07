# pemcast

`pemcast` watches versioned TLS certificate bundles in etcd, validates them,
atomically activates them as local release directories, and invokes a trusted
local reload hook.

## Commands

```text
pemcast agent
pemcast agent --once
pemcast config example
pemcast config validate
pemcast version
```

Configuration uses `cfgm`: defaults are overridden by the first default config
file, an explicit `--config/-c` file, `PEMCAST_*` environment variables, and
explicit command flags, in that order. Copy `config/config.example.yaml` to
`config/config.yaml` to start.

## Remote protocol

A publisher writes every immutable generation before changing its active
pointer:

```text
/pemcast/v1/active/nginx = 01K4GENERATION
/pemcast/v1/bundles/nginx/01K4GENERATION/manifest.json
/pemcast/v1/bundles/nginx/01K4GENERATION/files/fullchain.pem
/pemcast/v1/bundles/nginx/01K4GENERATION/files/privkey.pem
```

Example manifest:

```json
{
  "schema": "pemcast/v1",
  "files": [
    {
      "name": "fullchain.pem",
      "kind": "certificate",
      "sha256": "<lowercase sha256>"
    },
    {
      "name": "privkey.pem",
      "kind": "private-key",
      "sha256": "<lowercase sha256>"
    }
  ],
  "pairs": [
    {
      "certificate": "fullchain.pem",
      "private-key": "privkey.pem"
    }
  ]
}
```

Bundles are fetched at the revision of the snapshot or watch event. Each file
is SHA-256 verified, and the configured certificate/private-key pair must pass
`tls.X509KeyPair` before anything is activated.

## Local layout

For an output root such as `/etc/nginx/tls`, pemcast manages:

```text
/etc/nginx/tls/
├── current -> .pemcast/releases/sha256-...
└── .pemcast/
    └── releases/
        ├── sha256-old/
        └── sha256-current/
```

Applications should read paths below `current`, for example:

```text
/etc/nginx/tls/current/fullchain.pem
/etc/nginx/tls/current/privkey.pem
```

A release is completely written and synced before the `current` symlink is
atomically replaced. Old inactive releases are pruned according to
`retain-releases`.

## Hooks

The configured executable is run directly without a shell after activation.
It receives a JSON event on stdin and these environment variables:

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

A failed hook is recorded in the local target state. The next watch event or
periodic resync retries it without rewriting the already active release.

## Development

```bash
go test ./...
go run ./cmd/pemcast --help
go run ./cmd/pemcast --config config/config.yaml config validate
```
