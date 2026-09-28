# pemcast

pemcast 是一个从 etcd 拉取 TLS 证书的本地 agent. publisher 从证书内容计算 generation, 并在一个 etcd transaction 中原子提交完整单 key bundle 和 active pointer; pemcast 监听指针变化, 按事件 revision 取回 bundle, 校验 SHA-256 与证书/私钥匹配关系, 然后在本地生成不可变 release 并原子切换 `current` symlink. 应用始终读取稳定路径, hook 在切换成功后触发服务重载.

```mermaid
flowchart LR
    publisher["pemcast publish plan/apply"] -->|"原子提交 bundle + pointer"| etcd[("etcd")]
    etcd -->|"content-addressed pointer + revision"| agent["pemcast agent"]
    agent -->|"按 revision 获取并校验"| release["immutable release"]
    release -->|"原子切换 symlink"| current["current"]
    current --> app["nginx 或其他证书消费者"]
    agent -->|"JSON + 环境变量"| hook["可信本地 hook"]
    hook --> app
```

## 设计要点

- 证书发布者与消费者解耦: pemcast 不包含签发或审批系统, 但提供安全 `publish plan/apply/activate` 命令.
- 远端 generation 内容寻址: generation 由完整 bundle digest 计算, 相同内容天然相同, 内容变化必然得到新名字.
- 校验发生在启用前: manifest 严格解析, 文件逐一校验 SHA-256, 证书和私钥必须通过 `tls.X509KeyPair`, 并满足有效期策略.
- 本地发布是内容寻址的: release 目录名来自整个 bundle 的 digest, 相同内容不会重复写入. 复用 release 前会校验 marker, 文件内容, mode 和目录树.
- 应用路径稳定: 应用读取 `current/...`, pemcast 通过临时 symlink 和 rename 原子切换目标.
- output root 互斥: 非 dry-run agent 按排序获取每个 root 的 `.pemcast/agent.lock`, 并持有到进程退出.
- dry-run 无本地写入: 不创建 state directory 和 output root, 不读写 state, 不执行 deploy 或 hook.
- 服务重载可重试: hook 在本地切换后执行, 失败会记录在 state 中, 由后续事件或周期 resync 重试.

## 快速上手

生成并修改配置:

```bash
pemcast config example > config/config.yaml
```

至少配置 `agent.etcd.endpoints`, 一个 target 的 `output.root`, mappings, validation pair 和 hook, 然后校验:

```bash
pemcast --config config/config.yaml config validate
```

协议固定使用 `/pemcast/v2`. 以 target `nginx` 为例:

```text
/pemcast/v2/active/nginx = sha256-<bundle-digest>
/pemcast/v2/bundles/nginx/sha256-<bundle-digest> = complete JSON bundle
```

先生成显式发布计划. 首次发布使用 `--initial`; 后续发布必须声明当前 active generation:

```bash
pemcast --config config/config.yaml publish plan \
  --target nginx \
  --certificate fullchain.pem \
  --private-key privkey.pem \
  --expected-active-generation sha256-current \
  --output release-plan.json
```

再执行计划:

```bash
pemcast --config config/config.yaml publish apply --plan release-plan.json
```

`plan` 会读取 active pointer 的 generation 和 ModRevision, 并校验本地证书/私钥能组成 TLS pair. `apply` 重新读取本地文件, 确认 digest 未变化, 然后在一个 etcd transaction 中同时创建新 bundle 和切换 pointer. transaction 条件包含 bundle absent, active generation match 和 active ModRevision match. 任一条件失败时, bundle 和 pointer 都不会提交.

单 key JSON bundle 将文件 base64 内联, 完整 encoded value 最大 1 MiB. 回滚使用 `publish activate`, 它校验已有 bundle 和 TLS pair 后, 用 value + ModRevision CAS 切换 pointer.

先不落盘测试远端内容:

```bash
pemcast --config config/config.yaml agent --once --dry-run
```

再执行一次同步或进入监听模式:

```bash
pemcast --config config/config.yaml agent --once
pemcast --config config/config.yaml agent
```

本地布局为:

```text
/etc/nginx/tls/
├── current -> .pemcast/releases/sha256-...
└── .pemcast/
    ├── agent.lock
    └── releases/
        ├── sha256-old/
        └── sha256-current/
```

应用读取:

```text
/etc/nginx/tls/current/fullchain.pem
/etc/nginx/tls/current/privkey.pem
```

配置会拒绝重复或祖先/后代重叠的 output root. 同一个 root 的第二个 agent 进程会因文件锁立即失败.

## Hook 契约

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

hook 必须幂等. 失败时新证书文件可能已经启用, pemcast 会记录失败并在下一次 watch 事件或周期性 resync 时重试, 且不会重写同一个 release.

hook 默认只获得固定 `PATH` 和 `PEMCAST_*` 事件变量. 需要继承其他环境变量时, 在 `hook.pass-environment` 中显式列出变量名. hook 输出最多收集 4096 bytes, 超时会终止整个 process group.

## 命令

```text
pemcast agent
pemcast agent --once
pemcast agent --once --dry-run
pemcast config example
pemcast config validate
pemcast publish plan
pemcast publish apply
pemcast publish activate
pemcast status --json
pemcast version
```

配置由 `cfgm` 加载, 优先级为: 默认值, 第一个可用默认配置文件, 显式 `--config` 文件, `PEMCAST_*` 环境变量, 显式命令行参数.

## 项目 skills

仓库在 `.agents/skills/` 中包含三个 Codex skills:

- `$repo-architecture`: 架构, 包边界, 同步不变量和失败行为.
- `$repo-deployment`: agent 配置, etcd bundle 发布, 回滚, 运维和诊断.
- `$repo-development`: Go 修改, 包级测试, 生成配置, 构建和发布要求.

详细 references 位于:

- `.agents/skills/repo-architecture/references/architecture.md`
- `.agents/skills/repo-deployment/references/operation.md`
- `.agents/skills/repo-deployment/references/publishing.md`
- `.agents/skills/repo-development/references/development.md`

## 开发

```bash
go test ./...
go run ./cmd/pemcast --help
go run ./cmd/pemcast --config config/config.yaml config validate
```

`config/config.example.yaml` 由 `internal/config` 测试生成. 修改配置 schema 后运行 `go test ./internal/config`, 并提交更新后的示例文件.
