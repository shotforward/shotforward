# 架构概览

本文描述 ShotForward 当前稳定的组件边界。具体数据表、接口字段与实现细节以代码和测试为准。

## 设计目标

- 本地项目是创作事实的唯一来源。
- Web、桌面与自动化入口共享同一套业务规则。
- 每次生成都能追溯输入、Provider、状态和输出版本。
- Agent 可以协作，但不能绕过用户确认和领域约束。
- Provider 可替换，工程数据不依赖单一模型服务。

## 组件关系

```text
Tauri desktop shell
        │ starts / discovers
        ▼
React Web UI ── localhost HTTP + SSE ──► shotforwardd
                                             │
                 ┌───────────────────────────┼──────────────────────────┐
                 ▼                           ▼                          ▼
          Application / Domain        SQLite + media             Runtime adapters
                 │                                                ├─ Codex / MCP
                 └─ jobs, changesets, export                      ├─ Providers
                                                                  └─ FFmpeg
```

### Desktop

Tauri 壳负责窗口、daemon 生命周期、原生选择器、安装与更新。它不包含另一套业务逻辑，也不直接修改项目数据库。

### Web

React UI 负责展示远端快照、收集草稿和发起命令。TanStack Query 管理服务端状态，SSE 用于任务与会话事件通知。前端不持有 Provider 密钥，也不直接访问项目文件。

### Daemon

`shotforwardd` 是应用核心，拥有：

- 项目、剧集、剧本、资产、故事板和媒体版本规则；
- SQLite 事务、文件导入、任务调度和失败恢复；
- Agent 会话、ChangeSet、Provider 与导出适配；
- 本机 HTTP API、鉴权、输入校验和审计信息。

## 数据布局

应用级数据库保存项目索引、Provider 配置元数据和本机设置。每个项目目录独立保存工程数据库与媒体，便于备份和迁移。

```text
application-data/
└── studio.db

project-root/
├── project.db
├── media/
├── imports/
├── exports/
└── .shotforward/
    └── staging/
```

凭据不写入这些数据库；仅保存系统安全存储中的引用或非敏感配置。

## 写入模型

普通界面操作通过命令式 API 进入应用层，在事务内验证不变量并写入数据库。长耗时操作创建持久化任务，记录输入快照、进度、失败类型和重试信息，再由后台执行器处理。

Agent 修改采用两阶段流程：

1. Agent 读取受限的项目上下文并提出 ChangeSet。
2. 用户预览差异；daemon 在应用时重新检查版本和领域约束。

这样可以避免模型直接写数据库，也能在项目已变化时阻止陈旧修改覆盖新内容。

## 生成与恢复

图片、视频和导出均以任务运行。Provider 返回的外部标识与状态会被持久化，进程重启后可以继续轮询、重试或给出明确失败原因。媒体先写入 staging，校验成功后再原子化进入项目目录并创建版本记录。

## 本地 HTTP 安全

daemon 仅绑定回环地址。初次启动使用短期配对码交换受保护会话 Cookie；写请求同时接受 Origin 与 CSRF 校验。媒体和文件路径经过规范化，且必须落在允许的根目录内。

## 依赖方向

依赖始终指向领域核心：

```text
HTTP / jobs / providers / persistence
                 │
                 ▼
          application services
                 │
                 ▼
               domain
```

领域层不依赖 HTTP、SQLite、模型 SDK 或桌面运行时。新增能力应复用这一边界，而不是在界面、Provider 或脚本中复制业务规则。
