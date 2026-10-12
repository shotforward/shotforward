<div align="center">
  <img src="web/public/brand/shotforward-wordmark.png" alt="ShotForward" width="380">

  <h3>Ideas to frames. Stories in motion.</h3>
  <p><strong>开源 AI 视频创作工作台，面向短片、动画与 Agent 辅助视频制作。</strong></p>

  <p>
    <a href="LICENSE"><img alt="Apache-2.0 license" src="https://img.shields.io/badge/license-Apache--2.0-D22128?style=flat-square"></a>
  </p>

  <p>
    <a href="https://shotforward.com/download"><strong>下载</strong></a>
    · <a href="#产品导览">产品导览</a>
    · <a href="#首次配置">开始使用</a>
    · <a href="#架构">架构</a>
    · <a href="#从源码构建">从源码构建</a>
    · <a href="CONTRIBUTING.md">参与贡献</a>
  </p>

  <p><a href="README.md">English</a> · <strong>简体中文</strong></p>
</div>

<br>

## 从剧本到最终交付

ShotForward 是一款开源、本地优先的 AI 视频创作工作台，面向 AI 短片、短剧、动画、广告、MV 与连续剧集创作者。它将剧本、角色资产、分镜故事板、AI 视频生成和 MP4 导出整合到同一个桌面项目中，支持从剧本到视频的完整创作工作流。

在多镜头视频制作中，可复用的角色与场景参考素材帮助创作者管理跨镜头的视觉连续性。每次生成都能追溯到输入，Agent 发起的修改可以预览、审核与撤销。

这是一套围绕作品组织的生产流程，而不是彼此割裂的提示词集合。

| 环节 | 持续关联的内容 |
| --- | --- |
| **剧本** | Markdown 编写与导入、场景导航和 Agent 辅助修订 |
| **资产** | 可复用的角色、地点、道具、视觉风格与图片版本历史 |
| **故事板** | Scene / Clip 顺序、镜头提示词、生成模式与绑定的参考素材 |
| **生成** | Provider 能力、持久化任务、输入快照、执行进度与可恢复失败 |
| **预览** | 按故事板顺序连续播放，并进行非破坏式入点/出点裁剪 |
| **交付** | 由 FFmpeg 在后台导出 H.264/AAC MP4 |

## 下载与安装

使用官方桌面版本无需准备开发环境。请从[官网下载页](https://shotforward.com/download)查看可用安装程序。目前提供 Mac Apple 芯片版本，Windows 版本待发布。

| 平台 | 系统要求 | 官方安装包 |
| --- | --- | --- |
| **macOS · Apple Silicon** | macOS 12 及以上，M 系列芯片 | [前往下载](https://shotforward.com/download) |
| **Windows · x64** | Windows 10 / 11，64 位 | 待发布 |

不支持 Intel Mac。

仓库中的社区构建工作流生成未签名安装程序，Windows 和 macOS 可能对这些构建显示“未知开发者”警告；安装前请核对随附的 SHA-256 文件。第三方生成服务可能需要独立账户、订阅、API Key 或额度，费用、内容政策、数据处理方式与输出权利以对应服务商条款为准。

## 首次配置

选择适合你的模型接入方式，按照下面的步骤完成配置。

| 接入方式 | 需要准备 | 配置入口 |
| --- | --- | --- |
| **ShotForward 云服务** | 账号、积分、一次性登录码 | 设置 → ShotForward 账号 |
| **本地 Codex** | 本机 Codex 登录、具备所需权限的 ChatGPT 订阅 | 运行时设置与 Agent 模型选择 |
| **自有视频 API** | 服务商账号、API Key、端点与模型 | Provider 设置 |

### 使用 ShotForward 云服务

1. 前往 [ShotForward 官网](https://shotforward.com)，注册账号并登录账户中心。
2. 在账户中心充值积分，并生成客户端配对码（网站与客户端中称为一次性登录码）。
3. 打开客户端「设置 → ShotForward 账号」，在登录码有效期内输入配对码，完成账户连接。
4. 选择云端 Agent、图片或视频模型后即可使用；可用模型与费用以账户中心和客户端当前显示为准。

云端配对码用于连接 ShotForward 账户，与从源码运行 daemon 时用于打开本地工作台的启动配对码不同。

### 使用本地 Codex

1. 在本机安装 [Codex CLI](https://github.com/openai/codex)，使用具有所需 Codex 权限的 ChatGPT 订阅账号登录。
2. 在客户端运行时设置中检查本地 Codex 状态，并在 Agent 中选择本地模型。Agent 和 Codex 图片能力沿用本机登录；实际可用能力与额度以账号权限为准。

### 配置自有视频 API

如需通过自己的账户生成视频，在客户端 Provider 设置中添加视频服务商，填写对应区域的 API Key、端点和模型，并验证配置后启用。

视频服务需要单独开通并向对应服务商付费；Codex 订阅不包含第三方视频 API 费用。配置详情见 [Provider 指南](docs/providers.md)。请勿分享配对码或 API Key，也不要把它们提交到源码仓库。

## 产品导览

一个工程贯穿完整制作流程：从剧本创作与视觉资产沉淀，到镜头生成、连续预览和最终交付。

<details>
<summary><strong>展开查看工作台截图</strong></summary>

<table>
  <tr>
    <td><img src="docs/images/script-agent.png" alt="剧本与 Agent 协作"></td>
  </tr>
  <tr>
    <td align="center"><sub><strong>剧本</strong> · 结构化创作与可审核的 Agent 协作</sub></td>
  </tr>
  <tr>
    <td><img src="docs/images/assets.png" alt="版本化资产管理"></td>
  </tr>
  <tr>
    <td align="center"><sub><strong>资产</strong> · 可复用、可追溯的角色、场景、道具与风格</sub></td>
  </tr>
  <tr>
    <td><img src="docs/images/storyboard.png" alt="故事板与视频单元"></td>
  </tr>
  <tr>
    <td align="center"><sub><strong>故事板</strong> · 镜头、参考素材、提示词与生成结果</sub></td>
  </tr>
  <tr>
    <td><img src="docs/images/preview-export.png" alt="连续预览与导出"></td>
  </tr>
  <tr>
    <td align="center"><sub><strong>预览与导出</strong> · 连续播放、裁剪与 MP4 交付</sub></td>
  </tr>
</table>

</details>

## 核心设计

### AI Agent 辅助创作与可审核变更

Agent 可以结合当前剧本、资产和故事板工作，但不能绕过应用规则直接写入工程。持久化修改会形成 ChangeSet，应用时根据工程最新 revision 重新校验，并留下可审核、可安全撤销的记录。

### 角色参考素材与多镜头视觉连续性

角色、地点、道具和风格可以跨镜头、跨剧集复用。图片与视频结果保留版本历史；切换当前版本不会破坏此前生成的媒体。

### 可追溯、可恢复的 AI 图片与视频生成

图片、视频与导出均作为持久化任务运行，记录冻结的输入快照、进度、稳定的失败类型和重试上下文。媒体通过校验后才进入工程，从创作意图、Provider 到最终结果的关系始终清晰。

### 本地优先的视频制作与灵活模型接入

项目数据库与媒体保存在你选择的目录中。本地 daemon 仅监听回环地址，凭据进入操作系统安全存储，而不是工程文件。向第三方生成服务发起请求时，所需输入会发送给你配置的 Provider，并受对应服务条款约束。

## 集成能力

ShotForward 让工程数据独立于单一模型厂商。当前代码包含以下适配器：

| 能力 | 当前适配器 |
| --- | --- |
| **Studio Agent** | 本机 Codex、ShotForward Runtime |
| **图片生成** | 基于 Codex 订阅的图片能力 |
| **视频生成** | Seedance、MiniMax H3、Wan |
| **媒体处理** | FFmpeg、ffprobe |

实际可用能力取决于 ShotForward 版本、Provider 账户权限、所在地区与服务状态；工作台只展示本地 daemon 已确认可用的功能。

## 架构

```text
ShotForward Desktop (Tauri)
          │
          │ localhost + protected session
          ▼
React Web UI ───────────► shotforwardd
                              ├── Application / Domain
                              ├── SQLite + project media
                              ├── Codex Runtime / MCP
                              ├── generation providers
                              └── FFmpeg / ffprobe
```

所有工程修改都经过同一条权威路径：

- **Tauri 桌面壳**负责窗口、daemon 生命周期、原生选择器和操作系统集成。
- **React UI**负责呈现服务端状态并提交命令，不直接访问工程数据库、文件或 Provider 凭据。
- **`shotforwardd`**统一负责领域规则、SQLite 事务、工程媒体、后台任务、Agent 会话与安全边界。
- 工程数据与应用级设置分开保存，单个工程更便于迁移和备份。

daemon 只绑定回环地址，并通过短期配对建立受保护的 Cookie 会话；写请求同时执行 Origin 与 CSRF 校验。完整边界模型见 [架构概览](docs/architecture.md) 与 [Provider 指南](docs/providers.md)。

## 从源码构建

### 环境要求

- Go `1.26`
- Node.js `22.21.1`
- Rust `1.85+`（仅桌面壳）
- FFmpeg 与 ffprobe（用于媒体处理与导出）
- 已安装并登录的 Codex CLI（使用本地 Agent 或基于订阅的图片能力时）

以下命令应在仓库的干净 checkout 中运行。桌面打包还需要 Windows 或 macOS 对应的标准 Tauri 构建环境。

### 构建 Web UI

```sh
cd web
npm ci
npm test
npm run build
```

生产构建会写入已忽略的构建目录 `daemon/internal/webui/dist`，并内嵌到 daemon 二进制文件中。从干净 checkout 编译或测试 daemon 前，请先构建 Web UI。

### 运行 daemon

```sh
cd daemon
go test ./...
go run ./cmd/shotforwardd
```

daemon 会输出回环地址和临时配对码。浏览器访问该地址并完成配对后即可进入工作台。

桌面发行包会包含 Web UI、daemon、Codex Runtime、FFmpeg 与 ffprobe。稳定版本标签会构建未签名的 Windows 与 macOS 安装程序，并自动发布到 GitHub Releases；维护者应按照[发行指南](docs/RELEASING.md)操作。

## 项目结构

```text
daemon/    Go 应用核心、领域规则、SQLite、任务、Agent 与本地 API
web/       React 工作台、客户端状态、国际化与前端测试
desktop/   Tauri 桌面壳、原生集成、图标与发行工具
docs/      架构、Provider、发行流程与截图
```

## 文档

| 文档 | 内容 |
| --- | --- |
| [架构概览](docs/architecture.md) | 组件职责、数据所有权与安全边界 |
| [Provider 指南](docs/providers.md) | 生成服务、凭据与能力模型 |
| [发行指南](docs/RELEASING.md) | Windows/macOS 构建、签名与发布 |
| [贡献指南](CONTRIBUTING.md) | 开发环境、测试和提交约定 |
| [安全策略](SECURITY.md) | 漏洞报告与本地安全模型 |
| [许可说明](LICENSING.md) | 允许使用方式与商业许可边界 |
| [贡献者许可协议](CLA.md) | 接受源码贡献所适用的条款 |
| [商标政策](TRADEMARKS.md) | ShotForward 名称、Logo 与兼容产品的使用规则 |

## 参与贡献

欢迎提交可复现的问题、体验反馈和范围明确的改进。涉及工程格式、领域模型、Provider 协议或安全边界的改动，请先发起 Issue 讨论。提交代码前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 联系我们

- **官网与云端账户入口**：[shotforward.com](https://shotforward.com)
- **产品咨询与商务合作**：[contact@shotforward.com](mailto:contact@shotforward.com)
- **问题反馈与功能建议**：[GitHub Issues](https://github.com/shotforward/shotforward/issues)

## 许可

ShotForward 公共客户端源码采用 [Apache License 2.0](LICENSE) 开源许可证。个人和组织均可使用、研究、修改和再分发，也可以将其用于商业或专有产品，但须遵守许可证中的声明、归属及变更标注要求。Apache-2.0 不要求衍生作品公开源码。

中文说明见 [LICENSE.zh-CN.md](LICENSE.zh-CN.md)，常见场景说明见 [LICENSING.md](LICENSING.md)，外部源码贡献适用[贡献者许可协议](CLA.md)，品牌使用规则见[商标政策](TRADEMARKS.md)。官方托管 API、云服务、账户、额度、算力与支持是独立服务，不由本仓库的软件许可证授权。ShotForward 也可以提供适用独立条款的版本与商业合作，咨询：[contact@shotforward.com](mailto:contact@shotforward.com)。

---

<div align="center">
  <sub>为需要连续性、掌控力，以及从创意可靠走向交付的创作者而构建。</sub>
</div>
