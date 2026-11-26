# 应用插座接口参考

## 连接与授权

`BASE` 为确认过的管理源，例如 `http://127.0.0.1:49101` 或受信的 HTTPS 地址；`API`、`ADMIN`、`MCP` 分别为 `apiPath`、`adminPath`、`mcpPath`。下表的 Host 指 HTTP 请求头，不是客户端来源；不要用它代替认证、入口访问控制或加密。同机用回环地址，远程用[授权的 SSH 隧道或 HTTPS 入口](operations.md#管理员连接)。

| 入口 | IP Host | 导航域名 Host | 凭据 |
| --- | --- | --- | --- |
| REST（默认 `/api`） | `apiMode=ip`（默认）或 `on` | `on` | 管理员或受限部署令牌；仅添加预设应用可用临时密钥。 |
| 后台（默认 `/admin`） | `apiMode=ip`、`on` 或 `off` | `on` | 管理员令牌。 |
| MCP（默认 `/mcp`） | `mcpMode=ip`（默认）或 `on` | `on` | 管理员或受限部署令牌。 |

REST、MCP 各自的 `off` 返回 `404`，`ip` 下访问导航域名返回 `403`；`apiMode: off` 不关闭 IP 后台，也不改变 MCP。应用子域名的同名路径仍转发到应用。

使用 `Authorization: Bearer <令牌>`，JSON 请求加 `Content-Type: application/json`。不要跟随携带凭据的重定向，也不要把凭据发送到应用公开 URL。下例假定令牌通过可信环境注入，从标准输入向 curl 提供认证头，避免令牌进入命令参数：

```sh
BASE=http://127.0.0.1:49101
API=/api
printf 'header = "Authorization: Bearer %s"\n' "$APP_PLUG_TOKEN" |
  curl --noproxy '*' -fsS --config - "$BASE$API/routes"
```

受限令牌须获授权域名，只能管理自己拥有的路由；新增或修改反代目标还需符合 IP/网段授权或旧节点端口授权。直接配置本机可执行文件和启动参数需要管理员。

## 应用 REST API

路径相对 `API`。实例最多 64 条路由，写入应用 JSON 最大 8 KiB。

| 方法和路径 | 成功 | 请求与行为 |
| --- | --- | --- |
| `GET /routes` | `200` | 返回当前身份有权管理的应用数组。 |
| `GET /routes/{domain}` | `200` | 返回单个应用；不存在为 `404`。 |
| `POST /routes` | `201` | 添加应用；已用域名返回 `409`，不会覆盖。 |
| `PUT /routes/{domain}` | `200` 更新；`201` 新建 | 提交完整配置，只更新时带 `If-Match: *`（不存在为 `412`）。正文 `domain` 如果提供，须与 URL 域名等价。改名用 `newDomain`，旧路由必须存在，新旧域名均须获授权。 |
| `DELETE /routes/{domain}` | `204` | 移除路由并停止托管进程；已有服务不受影响。不存在为 `404`。 |
| `POST /routes/{domain}/start`、`POST /routes/{domain}/stop` | `200` | 启停托管进程，返回当前配置及状态；无需请求体，不改变自启设置。对已有服务返回 `409`。 |

`domain` 用完整域名或单个短名（自动追加 `baseDomain`），不含协议、路径或端口；首页域名保留。`name` 最长 80 个 Unicode 字符，已有服务必填，托管省略时用域名；可选 `description` 最长 400 个字符。`address` 和 `startFile` 二选一：

```json
{"domain":"report","name":"报表","address":"127.0.0.1:8080"}
```

```json
{"domain":"report","startFile":"/path/to/demo-app/run","args":[],"port":8080,"autoStart":false}
```

- `address` 用私有或回环 IP 加端口，IPv6 写成 `[::1]:8080`；不用 URL 或主机名。回环地址指导航所在的网络命名空间（容器内通常是容器自身）。导航的监听端口不能作为上游端口。
- `startFile` 为服务所在主机上的可执行文件绝对路径；`args` 逐项传入，不经 Shell。应用自己监听端口；REST `port` 省略或为 `0` 时自动识别，MCP 自动识别时应省略 `port`。运行规则见[本机托管](operations.md#本机托管)。
- `owner`、`url`、`running`、`ready`、`upstream`、`fileManaged` 是响应字段，不能直接回填。`running` 表示进程运行，`ready` 表示已发现可用监听；托管就绪时才返回 `upstream`。`fileManaged: true` 的路由可启停，编辑或移除须修改策略文件。
- 保留旧节点路由时可 `PUT /routes/{alias}`（短别名），只提交 `node`、`port`、`name` 和可选 `description`。旧启动项仍可读取，但新请求不接受 `launcher`。

改域名不配置 DNS 或证书，详见[域名与反代](operations.md#域名与反代)。托管路由改名时旧域名下的进程会停止，按新路由的自启设置处理。

## 管理员 HTTP 接口

路径相对 `ADMIN`；必须使用管理员令牌。策略与路由的保存规则见[配置与持久化](operations.md#配置与持久化)。

| 方法和路径 | 请求与结果 |
| --- | --- |
| `GET /data` | `200`，后台快照（路由、节点、身份及当前设置，不含令牌摘要）。 |
| `POST /routes` | 应用 JSON，`204` 添加，不覆盖。 |
| `PUT /routes/{完整域名}` | 完整应用 JSON，`204` 修改；改名时正文 `domain` 填新域名。目标不存在时返回 `404`。 |
| `DELETE /routes/{完整域名}` | `204` 移除路由并停止托管进程。 |
| `POST /routes/{完整域名}/start`、`POST /routes/{完整域名}/stop` | JSON `{}`，`200` 返回状态。 |
| `POST /deploy-keys` | 应用 JSON，`201` 返回 `token`、`domain`、`expiresAt`，不添加路由。 |
| `PUT /settings` | 例如 `{"apiMode":"ip"}`，成功为 `204`。 |
| `PUT /settings/mcp` | 例如 `{"mcpPath":"/mcp"}`，成功为 `204`；`mcpMode` 由本机策略管理。 |
| `PUT /nodes/{名称}` | `ip`、`minPort`、`maxPort` 及 `create`（`true` 新建、`false` 更新），成功为 `204`。 |
| `DELETE /nodes/{名称}` | `204`；先迁移引用的旧路由与授权。 |
| `PUT /principals/{名称}` | `create`、`admin`，受限身份再填 `domains`、`networks` 或兼容的 `aliases`、`prefixes`、`nodes`。新建 `201`，返回仅显示一次的原始 `token`；更新 `204`。 |
| `POST /principals/{名称}/rotate` | `{"token":"替换为高熵令牌"}`，成功为 `204`，旧令牌立即失效。 |
| `DELETE /principals/{名称}` | `204`；不能删除当前身份或仍拥有路由的身份。 |

更新受限身份时，省略 `launchers` 会保留旧启动项授权；传 `[]` 撤销全部，传名称数组则替换。后台仅改应用名称或简介时，保留路由归属和旧启动项；修改域名、上游、启动文件、参数、端口或自启设置时，由管理员接管路由。

### 临时 API Key

管理员在后台「添加应用」中预填后生成，也可调用 `POST $BASE$ADMIN/deploy-keys`。签发要求部署 API 已启用。将回执的接口和域名交给接入方，另用安全渠道提供密钥。

密钥只可作为 Bearer 调用 `POST $BASE$API/routes`（CLI 为 `app claim`），正文仅为 `{"domain":"签发回执中的完整域名"}`。成功返回 `201` 及 `domain`、`url`，随即消费。签发后 15 分钟、服务重启、签发者轮换令牌或失去管理员权限均使密钥作废；恢复原凭据或权限也不会重新激活旧密钥。请求超时后先核对路由是否已添加，不盲目兑换。

## MCP

使用 **Streamable HTTP**（无状态、仅 POST、JSON 响应，无 SSE）。在客户端配置 `$BASE$MCP` 和 Bearer 请求头；拒绝非空 `Origin`。手动请求须使用 `Content-Type: application/json` 和 `Accept: application/json, text/event-stream`；初始化后发送协商所得 `MCP-Protocol-Version`。从旧版升级时，将客户端的 `/api/mcp` 更新为 `/mcp` 或实际 `mcpPath`。

先发送 `initialize`，再发 `notifications/initialized`，通过 `tools/list` 获取参数 schema，随后调用 `tools/call`。

| 工具 | 操作 |
| --- | --- |
| `list_routes`、`get_route` | 列表或按 `domain` 查询。 |
| `create_route` | 添加应用，使用上面的应用配置字段，不覆盖已有路由。 |
| `update_route` | 替换同域名完整配置，不存在时会新建。只更新现有路由或改名时，用 CLI 或 REST 条件更新。 |
| `start_route`、`stop_route` | 按 `domain` 启停托管应用。 |
| `delete_route` | 按 `domain` 删除路由；托管进程会一并停止。 |

JSON-RPC 返回结果不等于操作成功：检查 `result.isError`，数据或错误说明在 `result.content[0].text`。

## 常见错误

- `400` / `413` / `415`：检查 JSON 字段、请求大小与 `Content-Type`。
- `401` / `403`：核对令牌、授权和 Host 策略；MCP 同时检查 `Origin`。
- `404`：核对实例路径、访问模式和域名。
- `406`：MCP 请求的 `Accept` 必须同时包含 `application/json` 和 `text/event-stream`。
- `409` / `412`：读取实际状态，核对域名冲突、应用类型、启动错误或更新目标是否存在。
- 写入失败：检查服务日志及策略文件所在目录和状态目录的权限。