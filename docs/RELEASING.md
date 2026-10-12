# ShotForward 桌面发行指南

本文档面向拥有仓库 Release 权限的维护者。ShotForward 默认通过 GitHub Actions 构建并发布可下载的未签名社区安装程序；同一次构建还会保留可审计的原始载荷，供需要官方证书的维护者在受控设备上完成签名。

## 发行链路

语义化版本标签 `v*.*.*` 会触发总发行工作流；它并行调用 Windows 与 macOS 构建，只有所有目标均成功后才创建 GitHub Release。

| 工作流 | 职责 | 产物 |
| --- | --- | --- |
| `.github/workflows/release-unsigned-desktop.yml` | 协调构建并发布 Release | Windows 与 macOS 未签名安装程序、SHA-256 文件 |
| `.github/workflows/build-windows-desktop.yml` | Windows 10 / 11 x64 | NSIS `.exe`、WiX `.msi`、签名用 `.zip` 载荷 |
| `.github/workflows/build-macos-desktop.yml` | macOS 12+，仅 Apple Silicon（ARM64） | 一个 `.dmg`、签名用 `.tar.gz` 载荷 |

Release 安装程序的文件名明确包含 `unsigned`，并为每个平台附带 SHA-256 清单。公开仓库还会生成 GitHub/Sigstore build provenance attestation。

安装程序包含 React UI、`shotforwardd`、Codex Runtime、FFmpeg、ffprobe 和 Tauri 桌面壳。它们没有 Authenticode 或 Apple Developer ID 签名，也没有经过 Apple notarization，因此操作系统可能显示“未知开发者”警告。发布说明必须明确这一点，不能把它们描述为已签名或已公证版本。

平台工作流还会把以下原始载荷保存为 Actions Artifact，保留 30 天：

- 源码提交 SHA、版本、目标架构和 Actions 运行链接；
- 载荷内各文件的 SHA-256；
- 未打包的 Tauri 主程序与对应 sidecar。

原始载荷用于复现、审计或后续本地签名，不是最终用户安装包，也不会上传到 Release。

## 为什么保留原始载荷

- 未签名社区版本无需把官方证书放入 GitHub Actions。
- 社区可以核对安装程序、源码提交、校验和与 provenance。
- 签名脚本会验证载荷 SHA-256，并拒绝签署与当前 checkout 提交不一致的载荷。
- 如果未来发布签名版本，可以确保内部 sidecar 与外层安装器都经过签名，而不是只签安装器外壳。

这里的“本地签名”表示证书不进入公共 CI，并不表示完全断网：Windows RFC 3161 时间戳和 Apple notarization 仍需要受控的网络连接。

## 固定的第三方构建输入

工作流直接固定下载 URL 与 SHA-256，不需要配置 GitHub Actions Variables：

- FFmpeg/ffprobe：`shaka-project/static-ffmpeg-binaries` `n8.1.2-1`；
- Codex Runtime：OpenAI Codex `rust-v0.154.0`。

升级这些输入时必须同时审核来源、许可证、目标架构、运行自检和 SHA-256。当前 FFmpeg 构建包含 GPL 组件，Release 必须附上对应许可证、版权声明和完整对应源码或有效书面源代码要约。

## 1. 发布未签名社区版本

1. 确认 `master` 上的 Go、Web 与桌面测试通过，工作区干净且远端同步。
2. 在目标提交上创建并推送带注释的稳定版本标签，例如 `v0.2.9`。
3. 等待 **Release unsigned desktop installers** 工作流完成。它会调用两个平台构建，并验证以下五个 Release Asset 全部存在：
   - Windows x64 未签名 NSIS `.exe`、WiX `.msi` 和 SHA-256 文件；
   - macOS Apple Silicon 未签名 `.dmg` 和 SHA-256 文件。
4. 从 GitHub Release 下载文件，重新计算 SHA-256，并至少在一台真实 Windows x64 和一台真实 macOS Apple Silicon 设备上验证安装、启动和卸载。
5. 检查 Release Notes 是否明确标注“unsigned”，且固定的 FFmpeg 与 Codex Runtime 二进制来源、对应源码链接仍与工作流一致；随后可补充重要变更和已知限制。

公开仓库可以验证 Release Asset 的 provenance，例如：

```sh
gh attestation verify ShotForward-v0.2.9-windows-x64-unsigned-setup.exe \
  --repo shotforward/shotforward
```

两个平台构建工作流也支持 `workflow_dispatch`，用于验证安装程序和原始载荷，但手动运行不会创建 GitHub Release。只有稳定标签触发的总发行工作流会公开发布文件。

## 2. 可选：Windows 本地签名和打包

要求：

- 与载荷提交 SHA 完全一致的仓库 checkout；
- Windows 10/11 x64；
- Node.js 22.21.1、Rust stable、Windows SDK（含 `signtool.exe`）；
- Authenticode 代码签名 PFX；
- 首次运行 Tauri/NSIS/WiX 依赖时可访问网络。

在 PowerShell 中运行：

```powershell
$env:WINDOWS_CERTIFICATE_PASSWORD = "PFX 导出密码"
./desktop/scripts/sign-windows-release.ps1 `
  -Payload ./ShotForward-v0.2.0-windows-x64-unsigned.zip `
  -Certificate D:/secure/shotforward-code-signing.pfx
```

若不设置 `WINDOWS_CERTIFICATE_PASSWORD`，脚本会安全提示输入。可用 `-TimestampUrl` 覆盖默认的 DigiCert 时间戳服务。

脚本会：

1. 验证载荷 SHA-256 和源码提交；
2. 临时导入 PFX；
3. 签署 daemon、Codex、FFmpeg 和 ffprobe；
4. 使用 Tauri `bundle` 生成并签署 NSIS `.exe` 与 WiX `.msi`；
5. 使用 `signtool verify /pa /all` 验证内部程序和安装器；
6. 在 `release-output/windows-x64/` 生成最终安装包和 SHA-256；
7. 移除本次临时导入的证书和临时打包配置。

若证书由 Azure Artifact Signing、HSM 或硬件令牌托管，应将脚本中的 PFX 导入与 `signtool` 调用替换为供应商的签名命令，不能导出官方私钥迁就脚本。

## 3. 可选：macOS 本地签名、公证和打包

要求：

- 与载荷提交 SHA 完全一致的仓库 checkout；
- macOS 12 或更新版本；
- Node.js 22.21.1、Rust stable、Xcode Command Line Tools；
- `Developer ID Application` `.p12` 证书；
- Apple Developer Program 团队；
- Apple ID 的 app-specific password；
- 可访问 Apple notarization 服务。

设置公证凭据并运行脚本：

```sh
export APPLE_CERTIFICATE_PASSWORD='P12 导出密码'
export APPLE_ID='release@example.com'
export APPLE_PASSWORD='xxxx-xxxx-xxxx-xxxx'
export APPLE_TEAM_ID='ABCDE12345'

./desktop/scripts/sign-macos-release.sh \
  ./ShotForward-v0.2.0-macos-arm64-unsigned.tar.gz \
  /secure/DeveloperIDApplication.p12
```

若省略 `APPLE_CERTIFICATE_PASSWORD`，脚本会安全提示输入。

脚本会：

1. 验证载荷 SHA-256 和源码提交；
2. 把 `.p12` 导入一次性 Keychain，并自动识别 Developer ID identity；
3. 使用 Tauri `bundle` 对内部可执行文件和 `.app` 完整签名；
4. 提交 Apple notarization 并 staple；
5. 验证 codesign、Gatekeeper 和 stapled ticket；
6. 在 `release-output/macos/<架构>/` 生成最终 `.dmg` 和 SHA-256；
7. 删除一次性 Keychain、恢复原 Keychain 搜索列表并移除临时打包配置。

证书、PFX/P12 密码、Apple ID 密码和私钥不得写入仓库、Actions Artifact、构建日志或 Release 文件名。

## 4. 可选：发布签名安装包

1. 在真实 Windows x64 和 macOS Apple Silicon 设备上验证安装、首次启动、账户登录、Provider、生成、预览、导出和卸载。
2. 打开标签对应的 GitHub Release；如需先做封闭验证，可另建 Draft Release。
3. 上传签名后的 `.exe`、`.msi`、一个 macOS ARM64 `.dmg` 及各自 SHA-256 文件，并以清晰文件名与未签名版本区分。
4. 在 Release Notes 中记录源码标签、Actions 构建链接、FFmpeg 许可证与源码获取方式。
5. 再次验证 Windows 签名主体、Apple notarization 和下载后的 SHA-256；使用 Draft Release 时，验证完成后再发布。

官方签名会改变文件字节，因此最终安装包 SHA-256 与公开未签名载荷不同。这是预期行为。可验证关系由源码提交、载荷 manifest、GitHub provenance、签名证书和最终安装包 SHA-256 共同建立。
