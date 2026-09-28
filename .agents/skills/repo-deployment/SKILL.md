---
name: repo-deployment
description: "配置和运行 pemcast, 用 publisher 发布或回滚 etcd certificate bundle, 执行 dry-run 校验, 查看本地状态, 诊断启用或 reload-hook 失败. 不用于内部实现修改."
---

# pemcast 部署

运维任务使用此 skill. 只读取请求需要的 references:

- agent 配置, 启动, container 接线, 失败行为, 回滚或清理: 阅读 [references/operation.md](references/operation.md).
- 创建, 发布, 校验或回滚 etcd 中的 certificate generation: 阅读 [references/publishing.md](references/publishing.md).
- 涉及生产部署或 etcd mutation 时, 先确认确切目标环境和请求的变更. 优先使用 `agent --once --dry-run` 作为非写入验证步骤.

完整 immutable generation 写入并校验前, 绝不移动 active pointer. 每个 output root 或 target set 只保持一个 agent 进程执行 reconcile.
