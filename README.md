# 应用插座 · App Plug

![](src/server/web/shared/app-plug-lockup.svg)

**从目录，直达应用。**

开发与测试应用的统一入口。

打开导航首页，从目录直达已发布的应用。应用插座按原始 `Host` 反向代理到内网 HTTP 服务，也可启动和停止本机托管应用。不管理 DNS 或证书；命令为 `app-plug`（Windows 为 `app-plug.exe`）。

## 选择接入方式

| 需求 | 怎么接入 |
| --- | --- |
| 服务已经在本机或其他主机运行 | 直接使用原 URL 就不必注册。需要新域名时，登记应用域名、显示名称和私有或回环 `IP:端口`。导航只做反代，不启停目标。 |
| 由导航启动本机应用 | 选择「本机托管」，填写本机可执行文件的绝对路径、逐项启动参数、可选端口和「随导航自动启动」。不必提前登记启动项或修改策略文件。见[本机托管](skills/app-plug/operations.md#本机托管)。 |
| 请 AI 添加或修改应用 | 打开首页右上角的「接入提示词」，按添加、修改或临时 Key 选择任务，复制该任务的指令。凭据另行提供。若只允许 AI 添加一个预设应用，由管理员先在「添加应用」表单[生成临时 API Key](skills/app-plug/api.md#临时-api-key)；签发本身不添加应用。 |

接线台按编号列出入口域名和状态，底部汇总入口数和可达数。「可达」仅表示上游返回了 HTTP 响应；打开应用域名确认实际内容。首页与后台右上角的半圆图标打开外观设置，浏览器会记住选择。页脚显示运行版本。

## 快速开始

在目标平台用 Go 1.27.4 及以上版本从仓库根目录构建（仅使用标准库），或使用下方通过校验的 CI 构建包。Windows 把输出文件名改成 `app-plug.exe`：

```sh
go build -trimpath -o ./bin/app-plug ./src/cmd/app-plug
```

以下是 **Linux 用户级 systemd 新装**命令。先确认目标目录、用户级 unit 和监听端口未被已有部署使用。用构建包时，把 `./bin/app-plug` 换成解压后的二进制路径，并换用自己的域名。macOS、Windows 或其他不使用 systemd 的环境按[前台运行](skills/app-plug/operations.md#前台运行)操作。已有实例按[升级与回滚](skills/app-plug/operations.md#升级与回滚)保留配置、凭据和路由。

```sh
install -d -m 0700 "$HOME/.apps/app-plug"
install -m 0755 ./bin/app-plug "$HOME/.apps/app-plug/app-plug"
app_plug="$HOME/.apps/app-plug/app-plug"

"$app_plug" config init --base-domain example.com --home-host nav.example.com
"$app_plug" config check
"$app_plug" service install --now
"$app_plug" service status
```

新装默认监听 `127.0.0.1:49101`；在本机打开 `http://127.0.0.1:49101/admin`，用 `~/.apps/app-plug/admin-token` 中的原始管理员令牌登录，在「应用路由」中添加应用。远程管理优先使用 [SSH 隧道](skills/app-plug/operations.md#管理员连接)，不要将监听端口直接暴露到公网。

不依赖 DNS 验证首页响应（`Host` 改为实际 `homeHost`）：

```sh
curl --noproxy '*' -H 'Host: nav.example.com' http://127.0.0.1:49101/
```

域名实际可访问还需要 DNS、HTTPS 证书及入口反代。参见[域名与反代](skills/app-plug/operations.md#域名与反代)。

## 持续集成与构建包

发往 `main` 的 PR 和推送到 `main` 的提交会运行 [App Plug CI](https://github.com/mu-xiao-tan/app-plug/actions/workflows/ci.yml)。Linux、macOS、Windows 分别在 x86_64 和 ARM64 runner 上验证实际目标、检查 `gofmt`、执行 `go vet` 和 `go test`，再校验安装包的 SHA-256 并解包运行 `version`、`help` 和隔离的 `config init/check/set/check`。总检查 `App Plug CI required` 只有在六个作业均成功时才通过；以对应提交的运行记录为准，不把交叉编译计作原生验收。CI 不覆盖系统服务安装、签名/公证或真实部署。

从运行详情的 Artifacts 下载对应系统和架构的 `app-plug-<目标>`。外层 ZIP 内有 Linux/macOS 的 `.tar.gz` 或 Windows 的 `.zip`，以及 `SHA256SUMS`。在该目录用 Linux 的 `sha256sum -c SHA256SUMS` 或 macOS 的 `shasum -a 256 -c SHA256SUMS` 校验。Windows 用 `Get-FileHash -Algorithm SHA256 -LiteralPath <压缩包>`，对照 `SHA256SUMS` 中同名文件的摘要。校验失败不要解包或运行。构建包保留 14 天。正式下载请用 [Releases](https://github.com/mu-xiao-tan/app-plug/releases) 中同一版本的压缩包和 `SHA256SUMS`。匹配程序版本的受保护正式标签通过六个原生作业与独立审批后，工作流才自动发布；它不会创建标签或部署应用。维护者见[发版手册](docs/releasing.md)。

## 管理入口

| 入口 | 用途 |
| --- | --- |
| 管理后台（默认 `/admin`） | 用管理员令牌维护应用、部署身份、临时 API Key 与访问设置。 |
| `app-plug app` / REST（默认 `/api`） | 列出、查看、添加、修改、启停和删除应用，或兑换临时 API Key。见 [CLI 用法](skills/app-plug/operations.md#应用-cli)及 [HTTP 接口](skills/app-plug/api.md)。 |
| MCP（默认 `/mcp`） | 用支持 Streamable HTTP 的 AI 客户端管理应用，独立于 REST 的访问模式。见 [MCP 接入](skills/app-plug/api.md#mcp)。 |
| `app-plug config` / `app-plug service` | 管理本机策略与用户级 systemd 服务。见[常用运维](skills/app-plug/operations.md)。 |

运行 `app-plug <命令组> <命令> --help` 查看参数。将 [`skills/app-plug/`](skills/app-plug/) 整个目录安装到 AI 客户端的 Skills 目录，可按 `app-plug` Skill 接入和操作指定实例。

## 源码目录

- `go.mod`：仓库根目录的 Go 模块。
- `src/cmd/app-plug/`：可执行程序入口。
- `src/server/`：导航、反代、管理接口与配置；`web/home/`、`web/console/`、`web/shared/` 分别存放首页、后台和共享资源。
- `src/internal/cli/`：CLI 命令；`src/internal/procnet/`：各平台进程监听识别。
- [`examples/`](examples/)：配置文件、两层 Nginx 和用户级 systemd unit 示例。实际部署以运行中的配置和 unit 为准。

## 版权与许可

应用插座由木炭（[@mu-xiao-tan](https://github.com/mu-xiao-tan)）开发。

Copyright © 2026 木炭。保留所有权利。本仓库未附开源许可证。