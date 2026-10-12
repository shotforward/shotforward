# ShotForward licensing guide

ShotForward's public client source code is Open Source software licensed under the [Apache License 2.0](LICENSE), abbreviated **Apache-2.0**. The [Simplified Chinese explanation](LICENSE.zh-CN.md) is provided for convenience; the English Apache-2.0 text in `LICENSE` controls.

Apache-2.0 is a permissive license. It permits use, modification, commercial activity, sublicensing, and redistribution in source or object form. It does not require modified or derivative works to publish their source code. Redistributors must still satisfy the conditions in Section 4, including providing the license, marking modified files, retaining applicable notices, and carrying forward applicable attribution from `NOTICE`.

## What the repository license covers

Unless a file, material, or bundled third-party component states otherwise, Apache-2.0 covers the source code in this repository, including the desktop shell, local daemon, Web interface, built-in provider integrations, and official cloud API client code.

The repository license does not grant access to or rights in:

- the implementation of ShotForward's hosted API or cloud services;
- accounts, credits, model capacity, storage, support, or service-level commitments;
- third-party models, services, binaries, fonts, or media; or
- ShotForward names, logos, application icons, domains, or other brand identity beyond the limited uses allowed by Section 6 of Apache-2.0, applicable law, and the [trademark policy](TRADEMARKS.md).

The brand image files under `web/public/brand/` and `desktop/src-tauri/icons/` are excluded from the Apache-2.0-licensed Work. Their copyright and trademark rights are reserved as described in the trademark policy.

Hosted services are governed by their own terms. A client implementation, API schema, or interoperability layer in this repository does not imply that the corresponding hosted implementation is included in the repository license.

## Common scenarios

| Scenario | Status under Apache-2.0 |
| --- | --- |
| A creator makes and sells films, animation, advertisements, or short videos | Permitted |
| A freelancer or studio uses ShotForward to produce content for clients | Permitted |
| A company uses or modifies ShotForward inside its organization | Permitted |
| A person redistributes an unmodified ShotForward build | Permitted; include the license and applicable notices |
| A person modifies and redistributes ShotForward | Permitted; mark modified files and preserve the required license and notices |
| A company incorporates ShotForward into a proprietary product | Permitted; Apache-2.0 does not require publication of the proprietary source code |
| A developer publishes an independent plugin through a documented interface | Permitted; comply with the licenses of any code included in the plugin |
| A company offers a modified version only as a hosted service | Permitted; hosted use alone does not trigger a source-disclosure requirement |
| A company independently develops software with similar functionality without copying ShotForward code | Apache-2.0 does not apply to the independent implementation |
| A fork uses the ShotForward name, logo, or identity as if it were official | Not permitted by the trademark policy without written authorization |

These examples explain intended common cases but do not replace Apache-2.0 or legal advice.

## Outputs and user projects

The software license does not claim ownership of audiovisual output or project content merely because ShotForward was used to create it. Users may commercially exploit their output, subject to applicable law and the separate terms of model providers, source materials, performers, fonts, music, and other third-party rights.

Project files, prompts, uploaded media, and generated output remain subject to the user's rights and any applicable service terms. Use of an optional hosted service may require the limited processing rights described in that service's terms and privacy policy.

## Commercial offerings

ShotForward may offer separately licensed editions, hosted services, OEM arrangements, support, warranties, indemnities, or service-level commitments. Those offerings use their own terms and do not remove or reduce rights already granted for the Apache-2.0 community source.

A separate agreement may be appropriate for parties that want contractual support, an OEM relationship, additional warranties, or permission to use ShotForward branding. Commercial terms do not automatically include hosted API usage, model credits, enterprise features, support, or trademark rights unless the agreement expressly says so.

## Contributions

Apache-2.0 Section 5 applies to contributions intentionally submitted for inclusion in the Work unless a contributor explicitly states otherwise. ShotForward additionally accepts external code contributions under the [Contributor License Agreement](CLA.md). Contributors retain copyright in their contributions, while the CLA provides a consistent rights framework for long-term development, distribution, licensing, hosted services, and commercial editions.

## 中文说明

ShotForward 公共客户端源码采用宽松开源许可证 **Apache-2.0**。任何人都可以使用、修改、商业运营和再分发 ShotForward，也可以将其纳入专有产品；Apache-2.0 不要求公开修改或衍生作品的源码。

再分发时仍须遵守 Apache-2.0 第 4 节，包括提供许可证副本、标明修改过的文件、保留适用声明，以及传递 `NOTICE` 中适用的归属信息。Apache-2.0 还包含贡献者提供的必要专利许可及相应的专利诉讼终止条款。

本许可证覆盖桌面壳、本地 daemon、Web 界面、内置 Provider 集成和官方云 API 客户端源码，但不授权 Official 云端服务端实现、账户、额度、算力、存储、第三方模型或 ShotForward 品牌资产。

个人、工作室和企业均可使用 ShotForward 创作并销售影视、广告、动画、短视频等内容，也可以在组织内部使用和修改。软件许可证不会仅因用户使用 ShotForward 就对项目或成片主张所有权。

ShotForward 可以提供适用独立条款的商业版本、托管服务、OEM、支持或服务等级承诺。这些安排不会撤销社区已经获得的 Apache-2.0 权利。

外部源码贡献同时适用 Apache-2.0 第 5 节和[贡献者许可协议](CLA.md)。贡献者保留其贡献的著作权；CLA 为项目的长期维护、发行、授权和商业可持续发展建立统一的权利框架。品牌名称与 Logo 的使用适用[商标政策](TRADEMARKS.md)。

This guide is not legal advice and does not replace the Apache-2.0 text or a signed agreement.
