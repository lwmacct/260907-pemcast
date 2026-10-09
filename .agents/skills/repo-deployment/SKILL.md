---
name: repo-deployment
description: "配置和运行 pemcast, 用 pack 加 publish 发布或回滚 content-addressed etcd certificate bundle, 执行 dry-run 校验, 查看本地状态, 诊断启用或 reload-hook 失败. 不用于内部实现修改."
---

# pemcast 部署

运维任务使用此 skill. 只读取请求需要的 references:

- agent 配置, 启动, container 接线, 失败行为, 回滚或清理: 阅读 [references/operation.md](references/operation.md).
- 初始化 broad prefix RBAC, 或在明确需要更强隔离时选择 target 级 RBAC: 阅读 [references/operation.md](references/operation.md), 确认目标后优先运行 [scripts/init-rbac.sh](scripts/init-rbac.sh).
- 使用 `agent.etcd.prefix` / `--etcd-prefix` 作为租户或环境 namespace: 阅读 [references/operation.md](references/operation.md). 一个 prefix 隔离一组 v5 targets; 不同租户使用不同 prefix 和不同 etcd users.
- 创建, 发布, 校验或回滚 etcd v5 content-addressed generation: 阅读 [references/publishing.md](references/publishing.md).
- 需要应急或审计时使用手动 etcdctl helper: 阅读 [references/publishing.md](references/publishing.md), 并运行 [scripts/publish-v5.sh](scripts/publish-v5.sh).
- 涉及生产部署或 etcd mutation 时, 先确认确切目标环境和请求的变更. 优先使用 `agent --once --dry-run` 作为非写入验证步骤.

v5 immutable bundle 必须先 stage, active pointer value + ModRevision CAS 是唯一 commit point; 孤儿 bundle 是允许的中间状态. 每个 output root 或 target set 只保持一个 agent 进程执行 reconcile. agent 读取 active prefix 和授权 target 的 exact bundle keys.

应用容器必须挂载完整 output root, 不能挂载 `current`, `current` 下的文件或 Kubernetes subPath. 现阶段不向 etcd 回报设备状态.
