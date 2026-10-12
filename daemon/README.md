# shotforwardd

`shotforwardd` 是 ShotForward 的本地应用核心。它提供 Web UI 与 HTTP/SSE API，并拥有项目数据、领域规则、后台任务、Agent、Provider 和媒体导出。

## 环境要求

- Go 1.26
- Node.js 22.21.1（从干净 checkout 构建内嵌 Web UI）
- FFmpeg 与 ffprobe（媒体处理和导出）
- Codex CLI（使用本地 Agent 或订阅图片能力时）

## 运行

```sh
cd ../web
npm ci
npm run build
cd ../daemon
go run ./cmd/shotforwardd
```

进程启动后会输出回环地址与短期配对码。浏览器先通过 bootstrap 接口完成配对，后续请求使用受保护的会话 Cookie。

## 测试

```sh
cd ../web
npm ci
npm run build
cd ../daemon
go test ./...
```

需要真实服务或账户的集成测试默认跳过，请先阅读对应测试的环境变量说明，再按需启用。

## 数据

应用级数据和项目数据分开保存。每个项目拥有自己的 SQLite 数据库、媒体、导入、导出与 staging 目录；Provider 密钥仅进入操作系统安全存储。

## 边界

- HTTP、SQLite、Provider 和任务执行器依赖应用与领域层，反向依赖不允许。
- 所有持久化修改经过应用服务和事务校验。
- Agent 只能提交 ChangeSet，不能直接写数据库。
- 文件路径必须规范化并限制在授权根目录中。

完整说明见 [架构概览](../docs/architecture.md)。
