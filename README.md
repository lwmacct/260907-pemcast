# pemcast

`pemcast` 监听 etcd 中带版本的 TLS 证书包, 校验后将其原子启用为本地 release 目录, 并调用可信的本地重载 hook.

## 命令

```text
pemcast agent
pemcast agent --once
pemcast config example
pemcast config validate
pemcast version
```

配置由 `cfgm` 加载: 默认值依次被第一个默认配置文件、显式指定的 `--config/-c` 文件、`PEMCAST_*` 环境变量和显式命令行参数覆盖. 可以将 `config/config.example.yaml` 复制为 `config/config.yaml` 作为起点.

## 远端协议

发布者在修改 active 指针之前, 必须先写入每个不可变的 generation:

```text
/pemcast/v1/active/nginx = 01K4GENERATION
/pemcast/v1/bundles/nginx/01K4GENERATION/manifest.json
/pemcast/v1/bundles/nginx/01K4GENERATION/files/fullchain.pem
/pemcast/v1/bundles/nginx/01K4GENERATION/files/privkey.pem
```

manifest 示例:

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

Bundle 会按 snapshot 或 watch 事件对应的 etcd revision 获取. 每个文件都会经过 SHA-256 校验, 并且任何内容被启用之前, 配置的证书/私钥对必须通过 `tls.X509KeyPair` 校验.

## 本地布局

对于 `/etc/nginx/tls` 这样的输出根目录, pemcast 会管理:

```text
/etc/nginx/tls/
├── current -> .pemcast/releases/sha256-...
└── .pemcast/
    └── releases/
        ├── sha256-old/
        └── sha256-current/
```

应用应读取 `current` 下的路径, 例如:

```text
/etc/nginx/tls/current/fullchain.pem
/etc/nginx/tls/current/privkey.pem
```

release 会完整写入并同步后, `current` symlink 才会被原子替换. 旧的未启用 release 会依据 `retain-releases` 清理.

## 钩子

配置的可执行文件会在启用后直接运行, 不经过 shell. 它会从 stdin 接收一个 JSON 事件, 并获得以下环境变量:

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

失败的 hook 会被记录到本地 target 状态中. 下一次 watch 事件或周期性 resync 会重试它, 且不会重写已经启用的 release.

## 开发

```bash
go test ./...
go run ./cmd/pemcast --help
go run ./cmd/pemcast --config config/config.yaml config validate
```
