---
name: repo-architecture
description: "解释, 评审或修改 pemcast 的架构, 包边界, 同步不变量或失败行为. 不用于日常部署操作或纯测试修改."
---

# pemcast 架构

当任务需要理解 pemcast 的设计原因, 或修改跨越包边界的行为时使用此 skill.

1. 解释或修改设计前, 先阅读 [references/architecture.md](references/architecture.md).
2. 将 etcd 协议和本地启用不变量视为契约. 如果请求的修改会削弱其中一个, 明确说明后果并提出更安全的设计.
3. 用相关包的针对性测试加 `go test ./...` 验证架构修改. 如果 `internal/reconcile` 发生实质变化, 补充 controller 级测试, 因为该包当前没有直接测试.

当前运行约束: etcd namespace prefix 可配置, 默认 `/pemcast`; 固定子协议是 `/v5/active/<target-id>` 与 `/v5/bundles/<target-id>/<generation>`; generation 必须由 bundle digest 推导; bundle 必须先 immutable stage, active pointer value + ModRevision CAS 是唯一 commit point, 允许孤儿 bundle; agent snapshot 使用 active prefix range read, watch 使用单个 active prefix watcher, bundle fetch 使用 exact key; 非 dry-run agent 由每个 output root 的排他文件锁保护. 应用容器必须挂载 output root, 不能挂载 `current` 或 Kubernetes subPath. 现阶段不向 etcd 回报设备状态. 修改任一语义时必须同步测试与文档.
