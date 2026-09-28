# pemcast 运维

## 配置

```bash
pemcast config example > /etc/pemcast/config.yaml
pemcast --config /etc/pemcast/config.yaml config validate
```

生效值的加载顺序是 defaults, 第一个默认文件, explicit config, `PEMCAST_*` 环境变量, 最后是 CLI flag. 环境变量 key 使用完整 schema path, 并把 `.` 和 `-` 替换为 `_`:

```text
PEMCAST_AGENT_ETCD_ENDPOINTS='["https://etcd-1:2379"]'
PEMCAST_AGENT_ONCE=true
PEMCAST_AGENT_WATCH_RESYNC_INTERVAL=10m
PEMCAST_AGENT_TARGETS='[{"id":"nginx",...}]'
```

scalar 和 duration 是字符串. struct, slice 和 map 是 JSON 文档.

配置 etcd 访问, 每个消费者一个 target, output root, 安全 mappings, validation pair 和 hook. agent 用户需要对 state 和 output path 有持久写权限. 配置会拒绝重复或祖先/后代重叠的 output root. 非 dry-run agent 会在每个 root 下创建 `.pemcast/agent.lock` 并持有到进程退出, 第二个进程会立即失败.

## 校验与启动

```bash
pemcast --config /etc/pemcast/config.yaml agent --once --dry-run
pemcast --config /etc/pemcast/config.yaml agent --once
pemcast --config /etc/pemcast/config.yaml agent
```

使用失败自动重启的 supervisor, 并用 `SIGTERM` 或 `SIGINT` 停止. 除非有意在首启同步, state 和 output path 都应持久化.

container 示例:

```bash
docker run --rm \
  -v /etc/pemcast/config.yaml:/app/data/config/config.yaml:ro \
  -v /etc/nginx/tls:/etc/nginx/tls \
  -v /var/lib/pemcast:/var/lib/pemcast \
  ghcr.io/lwmacct/260907-pemcast:<tag> \
  pemcast --config /app/data/config/config.yaml agent
```

如果 hook 必须影响 host, 使用 host 级 agent, 或显式挂载并授权一个可以跨越 container 边界工作的 hook.

## Hook 契约

executable 直接执行, 不经过 shell, 并接收:

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

stdin 中的 JSON 使用 `target-id`, `generation`, `previous-generation`, `etcd-revision`, `release-dir`, `current-dir`, `changed-files`, `bundle-sha256` 和 `activated-at` 字段. hook 在启用后运行, 必须幂等.

hook 默认只获得固定 `PATH` 和 `PEMCAST_*` 变量. `hook.pass-environment` 是显式放行的现有环境变量名列表. hook 输出收集上限为 4096 bytes, timeout 会终止整个 process group.

## 诊断

```bash
pemcast --config /etc/pemcast/config.yaml status --json
readlink -f /etc/nginx/tls/current
cat /etc/nginx/tls/current/.pemcast-digest
stat -c '%a %U:%G %n' /etc/nginx/tls/current/privkey.pem
cat /var/lib/pemcast/nginx.json | jq .
```

失败分类:

- etcd connect/read/watch: endpoint, authentication, TLS 或 compaction;
- fetch/hash/manifest/TLS validity: 远端 bundle 畸形或不完整;
- lock: 同一个 output root 已有 agent 进程;
- deploy: output 权限, release 完整性或文件系统失败;
- hook timeout/nonzero: executable, authorization 或下游服务失败.

回滚时把 active pointer 移回一个完整的旧 generation. 需要时 pemcast 会重建已被 prune 的本地 release. 删除 pointer 永远不会删除本地文件: `retain` 继续提供服务, `fail` 报告删除.

手工恢复前先停止持有 `agent.lock` 的 agent, 修复完整 release 和 symlink, 再重启并对目标 generation 执行 dry-run. 正常运行期间不要编辑 `.pemcast` 内部结构.
