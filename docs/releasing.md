# App Plug 发版手册

正式发版只改 `src/server/server.go` 的 `Version`（`vMAJOR.MINOR.PATCH`），通过 PR 合入 `main`。PR 不发布；主分支版本不变也不发布。版本降低或格式不合法时检查失败。

## 一次性配置

1. 保留 `main` 的 PR 和 `App Plug CI required` 检查。保留禁止更新、删除 `v*` 标签的独立规则。
2. 安装专用 GitHub App，只选 `mu-xiao-tan/app-plug`，仓库权限仅 Contents: read and write（另有 GitHub 必需的只读 Metadata）。把该 App 添加到 **Restrict release tag creation v*** 规则的 Always bypass；不要给分支保护或标签更新、删除规则添加例外。
3. 创建 `github-auto-release` 环境，只允许分支 `main`（无标签规则），不设逐次审核。把 `APP_PLUG_RELEASE_APP_CLIENT_ID` 配为环境变量，把 `APP_PLUG_RELEASE_APP_PRIVATE_KEY` 配为环境 secret；不要放在仓库级或组织级。这个发布作业是唯一取用凭据的作业，PR 和构建作业只读。旧 `github-release` 环境不参与新流程。

## 发版与验收

主分支同一次运行通过六个原生构建及 SHA-256 校验后，发布作业为该次推送的 SHA 创建正式标签，把六个包及 `SHA256SUMS` 上传到草稿，逐个核对 GitHub 返回的摘要后公开。发布说明包含提交 SHA 和运行链接。手动推标签不触发这套流程。

核对公开 Release、标签指向和 Actions 运行 SHA 一致，从 Release 下载全部七个文件并按清单校验。签名、公证、系统服务安装和实际部署另做验收。

已发布的版本不覆盖，指向其他提交的标签不移动。遇到暂时上传失败，优先重跑失败作业，复用原运行产物；同一运行的草稿只补传缺失且可验证的文件。遇到无法验证的草稿或标签冲突，先查明原因，不要强推或手工替换产物。要暂停自动发布，可以停用专用 App 的安装或移除环境私钥。
