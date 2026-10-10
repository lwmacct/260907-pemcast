# pemcast 运维

## 配置

```bash
pemcast config example > /etc/pemcast/config.yaml
pemcast --config /etc/pemcast/config.yaml config validate
```

生效值的加载顺序是 defaults, 第一个默认文件, explicit config, `PEMCAST_*` 环境变量, 最后是 CLI flag. 环境变量 key 使用完整 schema path, 并把 `.` 和 `-` 替换为 `_`:

```text
PEMCAST_AGENT_ETCD_ENDPOINTS='["https://etcd-1:2379"]'
PEMCAST_AGENT_ETCD_PREFIX=/pemcast
PEMCAST_AGENT_ONCE=true
PEMCAST_AGENT_WATCH_RESYNC_INTERVAL=10m
PEMCAST_AGENT_TARGETS='[{"id":"nginx",...}]'
```

scalar 和 duration 是字符串. struct, slice 和 map 是 JSON 文档.

配置 etcd 访问, 每个消费者一个 target, output root, 安全 mappings, validation pair 和 hook. agent 用户需要对 state 和 output path 有持久写权限. 配置会拒绝重复或祖先/后代重叠的 output root. 非 dry-run agent 会在每个 root 下创建 `.pemcast/agent.lock` 并持有到进程退出, 第二个进程会立即失败.

## etcd prefix, 租户和 RBAC 授权

etcd namespace prefix 默认是 `/pemcast`, agent 可以通过 `agent.etcd.prefix` 或 `PEMCAST_AGENT_ETCD_PREFIX` 修改, pack CLI 使用 `--etcd-prefix`. 程序只硬编码 `/v5/active|bundles` 子协议. 以 target `nginx` 为例:

```text
/pemcast/v5/active/nginx
/pemcast/v5/bundles/nginx/<generation>
```

`etcd-prefix` 可以作为多租户或多环境的 namespace 边界. 例如租户 `example` 使用 `/pemcast/tenants/example`, 租户 `demo` 使用 `/pemcast/tenants/demo`:

```text
/pemcast/tenants/example/v5/active/nginx
/pemcast/tenants/demo/v5/active/nginx
```

两个路径中的 `nginx` 是彼此隔离的 target, 不会共享 active pointer 或 bundles. 建议每个租户使用独立的 agent/publisher users; 一个 pemcast agent 进程只配置一个 prefix, 跨租户消费时运行多个 agent 进程或逐租户执行命令. prefix 不参与 bundle digest, 也不改变 `/v5` 协议语义.

当前生产使用 broad prefix RBAC. `agent` role 对整个 namespace prefix 只读, `publish` role 对整个 namespace prefix 读写:

```text
agent   read        /pemcast/ --prefix
publish readwrite   /pemcast/ --prefix
```

这个模式允许后续新增 target 或协议版本时不需要同步修改 etcd RBAC. agent 可以读取同一 prefix 下全部私钥 bundle, 因此该 prefix 内的 agent 与 publisher 都属于同一个信任边界. agent 仍然没有写权限. publisher user 供 `pemcast publish` 使用, 读取 active pointer 和 bundle, stage 新 bundle, 并 CAS 切换 pointer. root 只用于认证和用户管理, 不进入 pemcast 配置.

使用 skill 提供的脚本初始化. 脚本要求 etcd auth 已启用, 并交互读取 root, agent user, publisher user 三个密码; 既有用户不会被重置密码. TLS 参数复用 etcdctl 的 `ETCDCTL_CACERT`, `ETCDCTL_CERT` 和 `ETCDCTL_KEY` 环境变量.

```bash
ETCDCTL_ENDPOINTS='https://etcd.example:2379' \
bash .agents/skills/repo-deployment/scripts/init-rbac.sh \
  --etcd-prefix /pemcast \
  --broad \
  --agent-user agent-node-a \
  --publisher-user publisher-ci \
  nginx api.example.com
```

broad 模式会验证授权 active/bundle 可读, publisher 可写 bundle probe, agent 写入被拒绝, 以及未认证读取被拒绝. target 参数仅用于验证, 不缩小权限.

如果要把私钥 bundle 按 target 隔离, 可以省略 `--broad` 使用 target 级模式:

```text
agent-active:/pemcast/v5        read        /pemcast/v5/active/ --prefix
agent-bundles:/pemcast/v5:nginx read        /pemcast/v5/bundles/nginx/ --prefix
publisher:/pemcast/v5:nginx     readwrite   /pemcast/v5/active/nginx
publisher:/pemcast/v5:nginx     readwrite   /pemcast/v5/bundles/nginx/ --prefix
```

target 级模式适合多个互不信任的 consumer 组; 每新增 target 都需要同步追加 role. 当前生产不使用该模式.

node agent 配置使用:

```yaml
agent:
  etcd:
    endpoints:
      - https://etcd.example:2379
    prefix: /pemcast
```

发布端复用 `agent.etcd` 配置. `pemcast tools pack` 使用 `--etcd-prefix`, `pemcast publish` 使用 `agent.etcd.prefix` 与同一组 etcd endpoint 和 TLS 配置. etcd 认证统一为 `username:password`; agent 依次读取 `ETCDCTL_USER_AGENT`, `ETCDCTL_USER`, publish 依次读取 `ETCDCTL_USER_PUBLISH`, `ETCDCTL_USER`. 手动 fallback 使用 etcdctl 标准环境变量.

使用公共可信 CA 签发的 etcd 服务端证书时, 通常不需要额外配置 `agent.etcd.tls.ca-file`. 如果 endpoint 是 IP 而证书只包含域名, 优先配置 `agent.etcd.tls.server-name` 为证书域名; 仅在证书过期等临时应急场景使用 `insecure-skip-verify`.

## 校验与启动

```bash
pemcast --config /etc/pemcast/config.yaml agent --once --dry-run
pemcast --config /etc/pemcast/config.yaml agent --once
pemcast --config /etc/pemcast/config.yaml agent
```

使用失败自动重启的 supervisor, 并用 `SIGTERM` 或 `SIGINT` 停止. 除非有意在首启同步, state 和 output path 都应持久化.

非 dry-run agent 会自动创建本地目录: `agent.state-dir` 固定为 0700, 每个 target 的 output root 与 `.pemcast` 使用该 target 的 `output.directory-mode`. 部署脚本不需要预创建这些路径. `--once --dry-run` 保持无本地写入, 不会创建 state 或 output path.

### 自引用服务首装

当 etcd 自身的 server TLS 也由 pemcast 管理时, 先通过外部签发流程获得真实证书并执行 `pemcast tools pack`, 再在 etcd 启动前执行:

```bash
pemcast --config /etc/pemcast/config.yaml \
  tools \
  seed --target etcd --pack-dir /secure/tmp/etcd-pack
```

seed 不访问 etcd, 不写 agent state, 不改变 active pointer. 它只物化本地 content-addressed release 和 `current`. 随后 etcd 只读挂载完整 output root, 与 agent 同时启动. pointer 缺失时使用 `delete-policy: retain` 的 agent 会保留本地 release 并等待首次 publish.

本地 `current` 指向不同 generation 时, seed 默认拒绝. 只有停止 agent 后的显式人工救援才使用 `seed --force`; 随后正常 publish 同一 pack, 再启动 agent. 不要用固定自签证书, 指纹 fallback 或 `insecure-skip-verify` 来隐藏这个首装边界.

## 部署分层

通用 pemcast 部署脚本只负责运行工具容器, 挂载配置, 持久化 agent 数据和提供 hook 所需的控制通道. target id, output root, mapping 和 reload 策略全部由配置文件决定; 不要在通用部署脚本中引用具体 target 或预检某个 target 的证书文件.

消费者部署脚本只表达自己的挂载语义, 例如把 pemcast output namespace 挂载到 `/etc/<consumer>/tls`; 具体使用哪个 target 由消费者配置决定. 首次部署时先启动或同步 agent, 再启动必须立即读取证书的消费者, 或在 agent 完成同步后 reload/restart 消费者. 自引用的 etcd 例外: 先用真实证书 pack 执行 seed, 再让 etcd 与 agent 同时启动.

## 容器部署拓扑

### 推荐拓扑: host-level agent

在设备 host 上运行 pemcast agent, 输出 root 使用 host path:

```text
/var/lib/pemcast/nginx/
├── current -> .pemcast/releases/sha256-...
└── .pemcast/
```

应用容器只读挂载完整 output root:

```bash
docker run --rm \
  -v /var/lib/pemcast/nginx:/etc/nginx/tls:ro \
  nginx:latest
```

应用读取:

```text
/etc/nginx/tls/current/fullchain.pem
/etc/nginx/tls/current/privkey.pem
```

挂载规则:

- 正确: 挂载 output root, 例如 `/var/lib/pemcast/nginx:/etc/nginx/tls:ro`.
- 正确: 消费多个 target 时挂载共同 output parent, 应用读取 `/etc/nginx/tls/<target-id>/current/fullchain.pem`.
- 错误: 挂载 `current`, 例如 `/var/lib/pemcast/nginx/current:/etc/nginx/tls`.
- 错误: 挂载 `current` 下的单个文件.
- 错误: Kubernetes subPath 指向 `current`.

原因: 挂载 root 时, 应用每次 open 都会解析 `current` symlink. 直接挂载 `current` 或 subPath 时, container runtime 可能在启动时固定旧 release, pemcast 后续切换 symlink 时应用看不到新证书.

选择容器内路径时使用消费者语义路径, 例如 `/etc/nginx/tls` 或 `/etc/api/tls`; 避免把主机数据目录的长路径原样暴露给应用, 也避免使用无归属的根目录挂载点.

### 可选拓扑: node agent container

如果 pemcast 必须以容器运行, 它应作为 node-level 控制组件, 而不是普通业务 sidecar:

```bash
docker run -d \
  --name pemcast \
  --restart unless-stopped \
  -v /etc/pemcast/config.yaml:/etc/pemcast/config.yaml:ro \
  -v /etc/pemcast/hooks:/etc/pemcast/hooks:ro \
  -v /var/lib/pemcast:/var/lib/pemcast \
  -v /var/run/docker.sock:/var/run/docker.sock \
  <pemcast-image-with-docker-cli> \
  pemcast agent --config /etc/pemcast/config.yaml
```

这个镜像必须能执行 hook 所需的 runtime CLI, 例如 `docker` 或 `podman`; CLI 可以内置在镜像中, 也可以在确认兼容后从受信任 host 只读挂载. 挂载 Docker socket, Podman socket 或 containerd socket 等价于高权限, 只应用于受信任的 node agent. 如果不接受该权限模型, 请使用 host-level agent 或让应用自身支持证书 reload.

### 受限拓扑: sidecar

pemcast 也可以作为业务 Pod 的 sidecar, 与应用共享同一个 output root. 这个模式只适合 hook 能够控制同 Pod 内应用的场景:

- 应用支持 file watcher 或 localhost reload API.
- Pod 开启 shared process namespace, hook 可以向应用进程发送信号.
- 应用和 pemcast 由同一个 supervisor 管理.

如果 sidecar 没有上述控制通道, 它只能更新文件, 不能可靠地重载其他容器中的进程. 大规模设备场景应优先使用 host-level agent 或 node agent container.

### 多消费者共享

同一台设备上多个容器可以只读挂载同一个 output root:

```bash
docker run -v /var/lib/pemcast/nginx:/etc/nginx/tls:ro gateway
docker run -v /var/lib/pemcast/nginx:/etc/api/tls:ro api
```

一个 fan-out hook 可以统一重载所有消费者. hook 是本机部署的一部分, 证书更新后由 pemcast 自动执行:

```bash
#!/bin/sh
set -eu

docker exec gateway nginx -s reload
docker kill --signal=HUP api
```

如果消费者需要不同权限或不同重载策略, 为它们配置不同 target 和不同 output root.

一个消费者需要多个 target 时, 可以只读挂载这些 target 的共同 output parent:

```bash
docker run \
  -v /var/lib/pemcast/output:/etc/nginx/tls:ro \
  nginx:latest
```

Nginx 在各自的 `server {}` 中选择具体 target:

```nginx
ssl_certificate     /etc/nginx/tls/example.com/current/fullchain.pem;
ssl_certificate_key /etc/nginx/tls/example.com/current/privkey.pem;
```

这种挂载不会固定 `current`; 每次应用 open 仍会经过对应 target root 的 `current` symlink.

## Hook 示例

systemd 服务:

```bash
#!/bin/sh
exec systemctl reload nginx
```

Docker 容器:

```bash
#!/bin/sh
exec docker exec gateway nginx -s reload
```

向 Docker 容器发送信号:

```bash
#!/bin/sh
exec docker kill --signal=HUP api
```

Podman 容器:

```bash
#!/bin/sh
exec podman exec gateway nginx -s reload
```

同容器 supervisor 模式:

```bash
#!/bin/sh
exec kill -HUP 1
```

简单的一两条命令可以直接使用 shell `path` 加 `args`, 不需要独立脚本文件:

```yaml
hook:
  path: /bin/bash
  args:
    - -c
    - |
      set -eu
      docker exec gateway nginx -t
      docker exec gateway nginx -s reload
```

当 hook 出现分支逻辑, 需要复用, 或需要独立测试时, 再拆成可执行脚本. 无论内联还是脚本, 都必须保持幂等.

所有 hook 都必须幂等. 如果 hook 失败, pemcast 保留已启用的 release, 记录本地 hook error, 并在下一次事件或 resync 时重试 hook. local state 只用于本机 retry 和诊断, 不向 etcd 回报设备状态.

## Hook 契约

hook 的 `path` 直接执行, 不额外经过 shell; 需要 shell 时显式把 `path` 配置为 `/bin/sh` 或 `/bin/bash` 并在 `args` 中传入 `-c` 与脚本. hook 进程接收:

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

hook 默认只获得固定 `PATH` 和 `PEMCAST_*` 事件变量. `hook.pass-environment` 是显式放行的现有环境变量名列表. hook 输出收集上限为 4096 bytes, timeout 会终止整个 process group.

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

回滚使用旧 pack 目录重新执行 `pemcast publish`, 把 active pointer CAS 回完整的旧 content-addressed generation. 需要时 pemcast 会重建已被 prune 的本地 release. 删除 pointer 永远不会删除本地文件: `retain` 继续提供服务, `fail` 报告删除.

手工恢复前先停止持有 `agent.lock` 的 agent, 修复完整 release 和 symlink, 再重启并对目标 generation 执行 dry-run. 正常运行期间不要编辑 `.pemcast` 内部结构.
