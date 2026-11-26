# 应用插座运维手册

以下命令使用 PATH 中的 `app-plug`，也可用实际二进制的绝对路径替换。`config` 和 `service install` 默认使用 `~/.apps/app-plug/config.json` 和 `~/.apps/app-plug/state`。已有部署以实际 unit 的 `ExecStart`、drop-in 和监听地址为准，不要按默认值重装。

## 管理员连接

默认管理地址为 `http://127.0.0.1:49101`，后台路径为 `/admin`。在同机浏览器用原始管理员令牌登录；受限部署令牌只能用于获授权的应用操作。远程优先建立 SSH 隧道，再在本机浏览器或客户端连接同一地址：

```sh
ssh -N -L 127.0.0.1:49101:127.0.0.1:49101 example-user@nav.example.com
```

若实例监听其他地址或端口，调整隧道目标；本机端口已占用时也可改变本地绑定。通过导航域名管理时，使用具备入口访问控制的可信 HTTPS，并按[访问模式](api.md#连接与授权)授权该 Host。浏览器只在当前页面内存保留后台令牌，不写 Cookie 或浏览器存储；主题偏好单独按来源保存。

## 应用 CLI

`app` 使用部署 API。默认访问回环 `49101` 和 `/api`；非默认入口用 `--url`（不带路径的 HTTP(S) 源）和 `--api-path`。令牌由 `--token-file` 读取，省略时读取 `APP_PLUG_TOKEN`。下例使用新装生成的管理员令牌文件；若已轮换，使用当前凭据，不重新初始化：

```sh
token_file="$HOME/.apps/app-plug/admin-token"
app-plug app list --token-file "$token_file"
app-plug app add --domain report --name 报表 --address 127.0.0.1:8080 --token-file "$token_file"
app-plug app update --domain report --description '内部报表' --token-file "$token_file"
app-plug app get --domain report --token-file "$token_file"
```

- 本机托管用 `--start-file`，用多个 `--arg=值` 逐项传参，可加 `--port`、`--auto-start`。用 `app start` 或 `app stop` 加 `--domain` 启停。
- `app update` 保留未指定字段；`--new-domain` 改名。传入的 `--arg` 整体替换参数列表；用 `--clear-args` 清空，不能和 `--arg` 同用。`--auto-start=false` 禁止下次自启，不停止当前进程。
- `app delete --domain <域名> --yes` 移除路由；`app claim --domain <回执域名>` 兑换通过凭据提供的临时 API Key。按 `app-plug app <命令> --help` 查看参数。

## 本机托管

- 导航以自己的账号运行 `startFile`，工作目录为启动文件所在目录。Windows 只接收可信的普通 `.exe`；Linux/macOS 的脚本须含 shebang。程序须保持前台运行，不脱离进程组或 Windows Job。参数不经 Shell；不要通过参数传递秘密。
- 应用自己启动 HTTP 监听，建议绑定 `127.0.0.1` 或 `::1`。导航从操作系统报告的 TCP 监听匹配受托管进程，不扫描网络。macOS 依赖 `/usr/sbin/lsof` 与运行账号可见的进程；Windows 环境若禁止嵌套 Job，则启不起来，不会降级为不受托管的进程。Windows 停止时终止整个 Job，不提供 POSIX 的优雅停止信号。填写 `port` 时只匹配该端口；有多个不同端口时须指定 `port`。无可用监听时应用域名返回 `503`。
- 通过后台、REST、MCP 或 CLI 新建的本机托管默认不自启；启用 `autoStart` 会在新建时，以及随后的每次导航启动时尝试启动应用。手动停止保留域名、配置与自启设置，不会因普通热重载而重启。手动启动也不改自启。
- 修改运行中应用的启动文件、参数或监听端口会重启它；移除托管路由或停止导航会停止其进程。异常退出后不自动重启。旧版 `localApps` 由策略文件维护，省略 `autoStart` 时延续默认自启行为。
- 配置与运行就绪是两件事：已登记程序的启动文件后来被移走时，该应用无法启动，但不阻断其他应用或管理后台。新登记或修改启动配置时仍需提供可执行文件。监听地址存在竞争、无法确定由哪个应用响应时，不发布该上游。

## 配置与持久化

| 配置项 | 用途 |
| --- | --- |
| `baseDomain` / `homeHost` | 短名追加 `baseDomain`；导航首页域名可为该基础域名本身，或符合别名规则的单级子域名（如 `nav.example.com`），不能登记为应用。 |
| `listenPort` | `1–65535`，默认 `49101`。前台 `-listen` / `service install --listen` 可用 `IP:端口` 覆盖；实际监听变化需重启。 |
| `adminPath` / `apiPath` / `mcpPath` | 默认 `/admin`、`/api`、`/mcp`；必须互不相同，格式为 `/` 加单段小写别名，无尾斜杠。修改后同步更新反代和客户端。 |
| `apiMode` / `mcpMode` | 独立使用 `ip`（默认）、`on` 或 `off`，见[访问模式](api.md#连接与授权)。后台「访问设置」可改 `apiMode` 和 `mcpPath`；`mcpMode` 从本机策略修改。 |
| `principals` | 身份名到权限与 `tokenSHA256` 的映射；至少一个 `admin: true`。普通身份按域名和 IP/网段授权。 |
| `nodes` / `launchers` / `localApps` | 兼容旧节点、启动项和文件声明的托管路由；新接入无需配置这些项。 |

`config init --base-domain <域名>` 为新实例创建策略、状态目录和权限 `0600` 的 `admin-token`，不覆盖已有策略或令牌文件。`config show` 隐藏令牌摘要，不能作为完整备份。用 `config set` 改常用字段，或用 `config apply --from <独立文件>` 导入完整策略；二者均先校验已有路由，再原子保存。非默认部署指定 `--config`、`--state-dir` 及实际的 `--listen` 覆盖；`config check` 不监听、不执行托管程序。

改完用 `app-plug service reload`，或对已有自定义 unit 用 `systemctl --user reload <unit>`（须配置 `ExecReload` 发送 `SIGHUP`）。校验失败保留上一份有效快照；实际监听变化需重启。信号送达不等于重载成功，确认日志和 HTTP 响应。

后台/API 配置变更先校验并原子保存，随后生效。路由写入状态目录的 `routes.json`，由接口维护，不手工编辑；文件尚未创建时没有已保存的 API 路由。已开始的反代请求使用请求开始时的目标。后台可写设置、身份和授权保存在策略文件。服务账户需可写策略文件**所在目录**和状态目录；每个状态目录只运行一个实例。配置与状态放在非 Web 目录，成套私下备份。避免后台与外部工具同时写策略；外部修改后先校验、重载，再从后台保存。

### 令牌与权限

- 每个部署身份使用独立高熵令牌，只授予必要的域名和应用 IP。IP/网段授权覆盖范围内可用端口；旧节点授权仍受原 IP 和端口范围限制。避免将管理服务纳入授权。管理员令牌能配置本机程序，按主机管理凭据保管。
- 后台新增身份时原始随机令牌只展示一次。在「部署身份」选择「修改令牌」，输入并确认 16–128 个字符的高熵新令牌（英文字母、数字和 `. _ ~ + / = -`）。保存后旧令牌立即失效；若轮换当前身份，当前后台页面继续登录。同步更新调用端持有的凭据文件；初始化时的 `admin-token` 不会被自动更新。
- 轮换时保持身份名不变。策略的 `tokenSHA256` 字段只保存原始令牌的 SHA-256 十六进制摘要，计算时不包含末尾换行。全部管理员令牌丢失时，由主机管理员生成新令牌并替换一个管理员的摘要，然后校验并重载或重启。
- 通过策略移除身份或收紧权限只限制后续管理操作，不会自动下线已发布路由；需要下线时由管理员显式删除。缩小节点端口范围或改监听端口若会使现有路由失效，先调整受影响路由，再加载新策略。

## 前台运行

裸命令默认读取当前目录的 `./config.json` 和 `./state`，与 `config`、`service install` 子命令不同。用 `config init` 初始化新实例后，也可显式用同一份策略和状态在前台启动（先确保相同实例未由 systemd 运行）：

```sh
app-plug -config "$HOME/.apps/app-plug/config.json" -state-dir "$HOME/.apps/app-plug/state" -check-config
app-plug -config "$HOME/.apps/app-plug/config.json" -state-dir "$HOME/.apps/app-plug/state"
```

使用 [`examples/config.json`](../../examples/config.json) 手动初始化时，先复制到私有目录并替换示例域名与凭据。需要由程序创建首个管理员时，将示例的 `principals` 改为 `{}`，创建私有状态目录并在实例停止时以相同 `-config`、`-state-dir` 运行 `-init-admin operator`。它只对没有管理员的配置生效，原始令牌只向终端输出一次，勿在共享日志或自动化脚本中执行；之后运行 `-check-config` 确认策略和路由。

Windows 在 PowerShell 中从解包目录执行以下命令。Windows 配置目录须在支持 ACL 的文件系统上；只在没有同名配置、状态或实例的新环境执行 `init`。

```powershell
.\app-plug.exe config init --base-domain example.com
.\app-plug.exe config check
$root = Join-Path $env:USERPROFILE '.apps\app-plug'
.\app-plug.exe -config (Join-Path $root 'config.json') -state-dir (Join-Path $root 'state')
```

Windows 修改磁盘配置后重启前台进程；macOS 可向前台进程发送 `SIGHUP` 或重启。应用不安装 macOS launchd 或 Windows SCM 服务。

## 用户级 systemd（仅 Linux）

`service install` 校验策略和状态，把**当前二进制的路径**注册进 unit，但不复制文件。先将二进制放到稳定路径。默认 unit 为 `app-plug.service`，安装会启用用户服务自启；加 `--now` 才立即启动。无用户会话时也需开机运行，由系统管理员配置 lingering。默认命令不修改系统级服务、DNS 或反代。

所有 `service` 命令可用 `--unit app-plug-<名称>.service` 选择实例。`start`、`stop`、`restart`、`reload`、`uninstall` 只操作识别出的、与 systemd 当前载入内容一致的 unit；若有自定义 unit 或 drop-in，用 `systemctl --user cat` 核对后直接管理。`service stop` 保留自启设置；`service uninstall` 停止服务、取消自启并移除已识别的注册，保留二进制、配置、令牌和数据。仅取消自启并停用而保留注册可用 `systemctl --user disable --now app-plug`。

```sh
systemctl --user show app-plug -p ExecStart -p FragmentPath -p DropInPaths
systemctl --user status app-plug
journalctl --user -u app-plug -n 50
```

若手动采用 [`examples/app-plug.service`](../../examples/app-plug.service)，它使用 `~/.local/bin/`、`~/.config/app-plug/` 和 `~/.local/state/app-plug/`。先校验相应路径，确保没有同名部署，再安装到用户 unit 目录并运行 `systemctl --user daemon-reload` 与 `systemctl --user enable --now app-plug`。

## 域名与反代

- DNS、证书和 Nginx 配置由部署方维护。两层 Nginx 配置见 [`examples/nginx.conf`](../../examples/nginx.conf)，分别放入各自的 `http` 上下文。通配域名与证书须覆盖应用和导航首页；`*.example.com` 不覆盖 `example.com` 本身。首页在基础域名本身时，两层 `server_name`、DNS 和证书需另覆盖该域名。其他完整域名同样须单独覆盖；登记路由不能绕过已匹配请求的其他站点。
- 两层都保留原始 `Host`、URI 和查询，WebSocket 也应透传 Upgrade。不要重复创建已有的通配站点，保持其他显式站点配置。同机反代示例目标为 `http://127.0.0.1:49101`，「发送域名」用 `$http_host`。
- 公网入口只接受预期域名，不要把 IP Host 或兜底流量转发到导航，也不要把外部 Host 改写成后端 IP。跨主机可用 `-listen <专用内网IP>:49101`，限制来源为内网 Nginx 和获授权的管理端。不要直接对公网开放导航监听端口或应用节点；外层 HTTPS 不会加密内网链路，不可信链路另用 TLS。
- 经 SSH 管理时，可在公网 Nginx 对**导航域名**的后台路径及其子路径返回 `404`（默认 `/admin`、`/admin/` 前缀）。启用导航域名管理时，使用可信 HTTPS 入口并限制来源（如内网或 VPN）。不要全局拦截其他应用域名同名的路径。
- 应用应能从 `/` 服务，并配置正确的公开 URL、Host / Origin、Cookie 和 WebSocket。导航保留外部 Host 和路径，固定 `X-Forwarded-Proto: https`，不信任客户端转发头，也不重写 HTML、`Location` 或 Cookie。应用 Cookie 不应设为共享父域；若入口鉴权使用 Cookie 或 `Authorization`，应在入口消费并剥离这些凭据，再按需要转发应用自己的凭据。

## 升级与回滚

1. 核对 unit 和实际监听，记录当前版本。备份现用二进制及对应策略、状态和需要改动的 unit，按私有文件保管；不要重新初始化或覆盖现用令牌。
2. 用新二进制对现用配置和状态做 `-check-config`，带上实际 `-listen` 覆盖（如有）。通过后把新文件放到安装目标所在目录，再原子替换旧文件并重启已有 unit；不必重新注册 unit。
3. 查看 unit 状态、实际监听和服务日志，再查首页页脚版本及代表性应用的公开 URL。首页探测约每 20 秒以公开 Host 发送一次 `HEAD /`，单次最多 2 秒；收到任何 HTTP 响应只说明上游可达。`502` / `504` 检查目标可达性和响应，`503` 检查本机托管进程与预期端口。
4. 若新版失败，恢复已备份的二进制及需要恢复的配置，重启并重复验收。仅在确需恢复数据时才用备份覆盖状态，避免丢失升级以来的路由变更。

### 旧版迁移

- v0.13.0 统一使用 `app-plug` 命令、`app-plug.service`、`APP_PLUG_TOKEN` 和 `skills/app-plug/`。从更早版本升级时，先记录实际 unit、监听参数和配置路径；用新程序校验后停止并取消旧 unit 自启，再迁移现有配置、凭据与状态目录到 `~/.apps/app-plug/`，注册并启动新 unit。原域名和接口路径不变；不要并行运行两个实例或重新初始化令牌。自动化脚本、MCP 客户端和 Skill 引用也应切换到新标识。
- 若旧配置将 `/mcp` 用作后台或 REST 入口且没有 `mcpPath`，升级前在策略文件中明确指定另一未使用路径（如 `"mcpPath": "/tools"`），保留原有客户端地址。用新二进制校验通过后再重启。
- v0.1.0 的独立管理端口已移除：去掉 `-admin-listen`。旧反代若指向端口 `20000`，改到 `49101`，或显式设置 `listenPort: 20000` 保留旧地址；同步调整 SSH 隧道目标。该次迁移不改持久化路由格式。
- 试用过 `apiIPOnly` 时移除该字段；原 `true` 改为 `apiMode: "ip"`，原 `false` 改为 `apiMode: "on"`。
- `apps.d`、`-config-dir`、`/<alias>/` 和 `upstreamPath` 已不支持。保留旧配置与 unit 的备份；移除或替换过时参数，建立新域名入口，确认应用从 `/` 提供服务，再逐条登记并验收。不要自动清理旧部署。