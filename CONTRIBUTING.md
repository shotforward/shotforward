# 参与 ShotForward 开发

感谢你愿意帮助改进 ShotForward。为了让讨论和评审保持高效，请让每次改动聚焦于一个明确问题，并在提交前完成与改动范围相匹配的验证。

## 提交问题

- Bug 报告请包含操作系统、ShotForward 版本、复现步骤、预期行为和实际行为。
- 产品建议请说明要解决的创作问题，而不只是具体界面方案。
- 涉及工程格式、领域模型、Provider 协议或安全边界的较大改动，请先发起 Issue 讨论。
- 安全问题不要提交公开 Issue，请按照 [安全策略](SECURITY.md) 私下报告。

请勿在 Issue、日志或截图中包含 API Key、访问令牌、配对码、个人路径或未公开素材。

## 开发环境

- Go 1.26
- Node.js 22.21.1
- Rust 1.85 或更高版本（仅桌面壳）
- FFmpeg / ffprobe（媒体处理与导出）

安装依赖并运行主要检查：

```sh
cd web
npm ci
npm run typecheck
npm test
npm run build
npm run format:check
```

```sh
cd daemon
go test ./...
```

```sh
cd desktop
npm ci
npm run tauri -- build --ci --no-bundle
```

## 架构约束

- 前端不直接访问数据库、文件系统或 Provider 凭据。
- daemon 拥有领域规则、项目事实、任务调度和持久化。
- Agent 的持久化修改必须通过可预览、可校验的 ChangeSet。
- 凭据不得写入项目数据库、日志、URL、遥测或测试夹具。
- Provider 必须显式声明能力；不要仅凭“OpenAI 兼容”推断图片或视频能力。

更多背景见 [架构概览](docs/architecture.md) 与 [Provider 指南](docs/providers.md)。

## Pull Request

提交前请确认：

- 改动范围清晰，没有混入无关格式化或生成文件。
- 新行为有对应测试；用户可见文案同时更新中文和英文资源。
- `go test ./...`、前端类型检查、测试和生产构建通过。
- 涉及 API、工程格式、Provider 或安全模型时同步更新文档。
- 提交信息简洁说明行为变化，而不是文件操作过程。

## 贡献许可

ShotForward 客户端源码采用 [Apache License 2.0](LICENSE)。Apache-2.0 是宽松的开源许可证，允许使用、修改、商业化和再分发，同时要求保留适用的许可证、版权、归属及变更声明；请在贡献前阅读[许可说明](LICENSING.md)。

为了建立清晰一致的贡献权利框架，并支持项目的长期维护、灵活发行与商业可持续发展，所有外部源码贡献者必须接受[贡献者许可协议](CLA.md)。贡献者保留其贡献的著作权。

提交 Pull Request 时，请在 PR 模板中明确确认：

- 你有权提交该贡献；
- 你已阅读并接受 CLA；
- 代表公司提交时，你已取得公司的必要授权。

如果无法接受 CLA，可以先通过 Issue 提供问题描述、复现步骤或产品建议，而不直接提交代码。
