---
name: repo-deployment
description: "配置和运行 pemcast, 用 publish plan/apply/activate 发布或回滚 content-addressed etcd certificate bundle, 执行 dry-run 校验, 查看本地状态, 诊断启用或 reload-hook 失败. 不用于内部实现修改."
---

# pemcast 部署

运维任务使用此 skill. 只读取请求需要的 references:

- agent 配置, 启动, container 接线, 失败行为, 回滚或清理: 阅读 [references/operation.md](references/operation.md).
- 创建, 发布, 校验或回滚 etcd v2 content-addressed generation: 阅读 [references/publishing.md](references/publishing.md).
- 涉及生产部署或 etcd mutation 时, 先确认确切目标环境和请求的变更. 优先使用 `agent --once --dry-run` 作为非写入验证步骤.

v2 新 bundle 与 active pointer 必须在同一个 etcd transaction 中提交. 每个 output root 或 target set 只保持一个 agent 进程执行 reconcile.
