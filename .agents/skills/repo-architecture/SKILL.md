---
name: repo-architecture
description: "解释, 评审或修改 pemcast 的架构, 包边界, 同步不变量或失败行为. 不用于日常部署操作或纯测试修改."
---

# pemcast 架构

当任务需要理解 pemcast 的设计原因, 或修改跨越包边界的行为时使用此 skill.

1. 解释或修改设计前, 先阅读 [references/architecture.md](references/architecture.md).
2. 将 etcd 协议和本地启用不变量视为契约. 如果请求的修改会削弱其中一个, 明确说明后果并提出更安全的设计.
3. 用相关包的针对性测试加 `go test ./...` 验证架构修改. 如果 `internal/reconcile` 发生实质变化, 补充 controller 级测试, 因为该包当前没有直接测试.

当前运行约束: 非 dry-run agent 由每个 output root 的排他文件锁保护. 修改锁语义时必须保持多 root 排序加锁和进程退出释放.
