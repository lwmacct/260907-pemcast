# pemcast 开发

## 基线

```bash
go test ./...
go run ./cmd/pemcast --help
go run ./cmd/pemcast config example
go run ./cmd/pemcast --config config/config.yaml config validate
go run ./cmd/pemcast --config config/config.yaml status --json
```

仓库当前没有可见 lint task 或 PR test workflow. 交付前在本地运行完整测试.

## 生成配置

`config/config.example.yaml` 由 `internal/config` 测试生成并纳入版本管理. 修改 schema, default, description, type 或 cfgm option 后运行:

```bash
go test ./internal/config
```

然后检查并提交更新后的 example. 不要把它当作 source of truth 手工编辑.

默认运行配置不携带 target; `ExampleConfig()` 专门为 `config example` 提供 nginx 示例. 修改时保持这两者分离.

环境变量值使用完整 schema path, 例如 `PEMCAST_AGENT_ETCD_ENDPOINTS` 和 `PEMCAST_AGENT_ETCD_PREFIX`. struct, slice 和 map 是 JSON; scalar 和 duration 是字符串.

## 修改地图

- CLI/生命周期: 检查 `cmd/pemcast`, `internal/appcmd/agent`, `internal/appcmd/pack` 和 `internal/appcmd/status`. 保持 signal cancellation, once/watch 行为和 output root 锁生命周期.
- 配置: 修改 `internal/config/config.go` 与 `validation.go`, 并覆盖 path 安全, mode, duration, target 唯一性, output root 重叠, mapping 引用和 hook 环境变量名.
- 远端协议: 保持可配置 etcd prefix, 固定 `/v5/active/<target-id>` 与 `/v5/bundles/<target-id>/<generation>` kind-first layout, active-prefix snapshot/watch, exact-key bundle fetch, 1 MiB 单 key bundle 限制, 严格 JSON, kind/encoding 校验, generation/digest 匹配, 以及从同一个 `snapshot + 1` revision 开始 watch. snapshot/watch 仍需真实 etcd 集成测试.
- bundle 校验: 保持 digest 逻辑独立于 etcd 和 deployment. 测试 manifest 拒绝, digest 顺序和稳定性, base64 解码, 文件 hash, key mismatch, kind mismatch, encoding mismatch, size limit 和 validity 边界.
- reconcile: 覆盖 changed/unchanged digest, dry-run, hook retry, state 更新, concurrency, snapshot 缺失 target 和两种 delete policy. 保持 consumer-side 接口便于 fake 注入.
- deployment: 保持排序 flock, staging, fsync, rename, content-addressed release, release 完整性校验, 原子 relative symlink 切换和 active-release 保留.
- hook/state: 保持直接执行, 最小环境, event schema, process group 超时, 输出限额, 严格 JSON 和原子 state 写入.
- pack: 保持本地 key pair 校验, content-addressed generation, deterministic canonical bundle, 安全输出目录, metadata 不暴露私钥, 以及可被 etcdctl 解析的 stage transaction. 远端发布不变量由 `scripts/publish-v5.sh` 维护: bundle 先 stage, existing bundle 必须 exact match, active pointer 是唯一 commit point.
- status: 保持只读, 不联系 etcd, 不创建目录, 不输出证书内容或凭据.

不要为尚未需要的能力提前增加接口; 新接口应从 controller 或命令的实际测试边界自然产生.

## 构建与发布

```bash
go build ./cmd/pemcast
```

GitHub publish workflow 会构建 static linux/amd64 binary, 注入 version 变量, 用 UPX 压缩, 校验 version, 构建并推送 GHCR image. 不要手工编辑生成的 version metadata.

交付前对已修改 Go 文件运行 `gofmt`, 开发中运行针对性测试, 最后运行 `go test ./...`, 检查受影响的 CLI/config 行为和生成配置变化. 协议或部署行为变化要同步更新对应 skill reference 与 README.
