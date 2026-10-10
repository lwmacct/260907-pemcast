---
name: repo-development
description: "修改, 调试或测试 pemcast 的 Go 实现, 同时保持 v6 单 key 协议, staged publication, 校验, 原子启用和生成配置规则. 不用于纯部署操作."
---

# pemcast 开发

代码和测试修改使用此 skill. 先阅读 [references/development.md](references/development.md), 再检查其中列出的相关包后动手. v6 只保留单一 `publish --pack-dir`; `upgrade` 只是一次式 canonical v5 active 到 v6 迁移, 不恢复 v4/v5 plan/apply/activate 或 runtime 旧协议兼容.

Go 修改的最低验证是 `go test ./...`. 配置 schema 修改还必须通过现有 config test 重新生成并提交 `config/config.example.yaml`.
