"use strict";

const $ = (id) => document.getElementById(id);
const { adminPath, apiPath } = document.body.dataset;
const modeNames = { ip: "仅 IP 地址", on: "IP 地址和导航域名", off: "停用部署 API" };
const resources = {
  route: { path: "routes", key: "route-domain", label: "应用" },
  node: { path: "nodes", key: "node-name", label: "节点" },
  principal: { path: "principals", key: "principal-name", label: "身份" },
};
const sectionButtons = $("sections").querySelectorAll("[data-section]");
const loginInput = $("login-token");
let session = null;
let snapshot = null;
let busy = false;
let settingsDirty = false;
let mcpPathDirty = false;
let rotationTarget = "";

function element(tag, className, text) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text != null) el.textContent = text;
  return el;
}

function message(id, text = "") {
  const el = $(id);
  el.textContent = text;
  el.hidden = !text;
}

function setBusy(value) {
  busy = value;
  for (const control of document.querySelectorAll("[data-task], .editor-fields")) {
    control.disabled = value;
  }
  $("login-submit").disabled = value;
  $("login-submit").textContent = value && $("workspace").hidden ? "正在验证…" : "登录";
  loginInput.disabled = value;
}

function switchSection(section) {
  for (const button of sectionButtons) {
    const active = button.dataset.section === section;
    $(button.getAttribute("aria-controls")).hidden = !active;
    button.setAttribute("aria-pressed", String(active));
  }
}

function resetSession(text, focus = true) {
  if (session) {
    session.token = "";
    session.controller.abort();
  }
  session = null;
  snapshot = null;
  settingsDirty = false;
  mcpPathDirty = false;
  closeRotation();
  loginInput.value = "";
  for (const type of Object.keys(resources)) {
    closeEditor(type);
    $(`${type}-list`).replaceChildren();
    messageValue(`${type}-count`, "");
  }
  $("node-grants").replaceChildren();
  for (const id of ["actor", "base-domain", "home-host", "listen-port", "api-summary", "settings-hint", "domain-hint"]) {
    messageValue(id, "");
  }
  $("settings-form").reset();
  $("mcp-settings-form").reset();
  for (const input of $("token-receipts").querySelectorAll("textarea")) input.value = "";
  $("token-receipts").replaceChildren();
  $("token-receipts").hidden = true;
  for (const id of ["activity", "workspace-error", "settings-warning", "settings-error", "mcp-settings-error"]) message(id);
  $("workspace").hidden = true;
  $("instance-panel").open = false;
  $("logout").hidden = true;
  $("signin").hidden = false;
  switchSection("routes");
  setBusy(false);
  message("login-error", text);
  if (focus) loginInput.focus();
}

async function request(current, path, method = "GET", body) {
  const headers = { Accept: "application/json", Authorization: `Bearer ${current.token}` };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let response;
  try {
    response = await fetch(`${adminPath}/${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: current.controller.signal,
      credentials: "omit",
      cache: "no-store",
      mode: "same-origin",
      redirect: "error",
    });
  } catch (error) {
    if (current.controller.signal.aborted) throw error;
    throw new Error(`请求失败：${error.message}。请检查连接和管理地址；控制台不会跟随重定向。`);
  }
  if (!response.ok) {
    const problem = await response.json().catch(() => null);
    const text = typeof problem?.error === "string" && problem.error ? problem.error : "请求未成功。";
    const error = new Error(`${text}（HTTP ${response.status}）`);
    error.status = response.status;
    throw error;
  }
  if (response.status === 204) return undefined;
  try {
    return await response.json();
  } catch {
    throw new Error("管理接口没有返回有效的 JSON。请检查代理和管理地址。");
  }
}

function rejectedSession(error) {
  return error.status === 401 || error.status === 403;
}

function ipGuidance(port) {
  return `请改用服务的 IP 地址访问 ${adminPath}（本机示例：http://127.0.0.1:${port}${adminPath}；远程请使用授权的 SSH 隧道），并重新登录。`;
}

function modeHint(mode) {
  const common = "仅 IP 和停用模式下，管理控制台仍可通过 IP 地址使用。";
  if (mode === "on") return "部署 API 和管理控制台均接受 IP 地址或导航域名。";
  if (mode === "off") return `停用 ${apiPath}，不影响当前管理后台的管理功能。${common}`;
  return `部署 API 仅通过 IP 地址访问。${common}`;
}

function updateModeHint() {
  const mode = $("api-mode").value;
  const warning = snapshot && !$("home-link").hidden && mode !== "on"
    ? `保存后，当前导航域名将无法访问管理后台。${ipGuidance(snapshot.listenPort)}`
    : "";
  messageValue("settings-hint", modeHint(mode));
  message("settings-warning", warning);
}

function messageValue(id, value) {
  $(id).textContent = value;
}

function refreshView(data) {
  snapshot = data;
  const routes = data.routes || [];
  const nodes = entries(data.nodes);
  const principals = entries(data.principals);
  messageValue("actor", data.actor);
  messageValue("base-domain", data.baseDomain);
  messageValue("home-host", data.homeHost);
  messageValue("listen-port", data.listenPort);
  messageValue("api-summary", modeNames[data.apiMode] || data.apiMode);
  $("home-link").hidden = location.hostname.toLowerCase().replace(/\.$/, "") !== data.homeHost;
  if (!settingsDirty) $("api-mode").value = data.apiMode;
  if (!mcpPathDirty) $("mcp-path").value = data.mcpPath;
  updateModeHint();
  updateDeployKeyActions();
  messageValue("route-count", routes.length);
  messageValue("node-count", nodes.length);
  const nodesButton = $("sections").querySelector('[data-section="nodes"]');
  nodesButton.hidden = nodes.length === 0;
  if (nodesButton.hidden && nodesButton.getAttribute("aria-pressed") === "true") switchSection("routes");
  messageValue("principal-count", principals.length);
  fillList("route-list", routes.map(renderRoute), "尚未添加应用", "可转发到已运行的服务，或配置本机托管。");
  fillList("node-list", nodes.map(([key, value]) => renderNode(key, value)), "尚未配置旧版节点", "没有旧版节点也可以手动添加应用。");
  fillList("principal-list", principals.map(([key, value]) => renderPrincipal(key, value)), "尚无部署身份", "可为管理员或部署任务新建身份。");
  for (const select of $("node-grants").querySelectorAll(".grant-node")) fillNodeSelect(select, select.value);
}

function entries(object) {
  return Object.entries(object || {}).sort(([a], [b]) => a.localeCompare(b));
}

function fillList(id, rows, title, hint) {
  const list = $(id);
  if (rows.length) {
    list.replaceChildren(...rows);
    return;
  }
  const empty = element("li", "empty");
  empty.append(element("h3", "", title), element("p", "", hint));
  list.replaceChildren(empty);
}

function actionButton(text, purpose, handler, dangerous = false) {
  const button = element("button", dangerous ? "quiet danger" : "quiet", text);
  button.type = "button";
  button.setAttribute("aria-label", `${text} ${purpose}`);
  button.dataset.task = "";
  button.disabled = busy;
  button.addEventListener("click", handler);
  return button;
}

function record({ title, type, details, note, buttons, url, detailFallback }) {
  const row = element("li", "record");
  const content = element("div");
  const heading = element("div", "record-title");
  heading.append(element("h3", "", title));
  if (type) heading.append(element("span", "record-type", type));
  content.append(heading);
  if (url) {
    const link = element("a", "record-url", url);
    link.href = url;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    content.append(link);
  }
  if (note) content.append(element("p", "record-note", note));
  const meta = details.length ? element("dl", "record-details") : element("p", "record-details", detailFallback);
  for (const [label, value, code] of details) {
    const detail = element("div", "record-detail");
    const description = element("dd");
    if (code) description.append(element("code", "", value));
    else description.textContent = value;
    detail.append(element("dt", "", label), description);
    meta.append(detail);
  }
  const actions = element("div", "actions");
  actions.append(...buttons);
  row.append(content, meta, actions);
  return row;
}

function httpsURL(value) {
  try {
    const url = new URL(value);
    if (url.protocol === "https:" && url.href.startsWith(`${url.origin}/`)) return url.href;
  } catch {
    // Do not turn an invalid route address into a navigation target.
  }
  return "";
}

function renderRoute(route) {
  const key = route.domain;
  const managed = route.managed;
  const details = [];
  const buttons = [];
  if (managed) {
    const status = route.running ? (route.ready ? "已就绪" : "运行中，等待上游就绪") : "已停止";
    details.push(["状态", status], ["端口", route.port ? `指定 ${route.port}` : "自动发现"]);
    if (route.upstream) details.push(["上游", route.upstream, true]);
    buttons.push(actionButton(route.running ? "停止" : "启动", `应用 ${key}`, () => setRouteRunning(key, !route.running)));
  } else {
    details.push(["上游", route.address, true]);
  }
  if (route.fileManaged) details.push(["配置来源", "仅可在服务器配置文件中编辑"]);
  if (!route.fileManaged) {
    buttons.push(
      actionButton("编辑", `应用 ${key}`, () => openEditor("route", key, route)),
      actionButton("删除", `应用 ${key}`, () => removeResource("route", key,
        managed ? "公开域名将停止转发，托管进程也会停止。" : "公开域名将停止转发。"), true),
    );
  }
  return record({
    title: route.name, type: managed ? "本机托管" : "上游转发", details,
    note: route.description, buttons, url: httpsURL(route.url),
  });
}

function portRange(value) {
  return `${value.minPort}–${value.maxPort}`;
}

function renderNode(key, node) {
  const details = [["IP", node.ip, true], ["端口", portRange(node), true]];
  const buttons = [
    actionButton("编辑", `节点 ${key}`, () => openEditor("node", key, node)),
    actionButton("删除", `节点 ${key}`, () => removeResource("node", key, "请先迁移引用此节点的路由及身份授权。"), true),
  ];
  return record({ title: key, details, buttons });
}

function renderPrincipal(key, principal) {
  const details = [];
  if (!principal.admin) {
    if (principal.domains?.length) details.push(["域名", principal.domains.join("、"), true]);
    if (principal.networks?.length) details.push(["IP / 网段", principal.networks.join("、"), true]);
    if (principal.aliases?.length) details.push(["精确别名", principal.aliases.join("、"), true]);
    if (principal.prefixes?.length) details.push(["别名前缀", principal.prefixes.join("、"), true]);
    for (const [node, range] of entries(principal.nodes)) details.push(["节点", `${node}：${portRange(range)}`, true]);
    if (principal.launchers?.length) details.push(["旧版启动项", principal.launchers.join("、"), true]);
  }
  const buttons = [
    actionButton("编辑", `身份 ${key}`, () => openEditor("principal", key, principal)),
    actionButton("修改令牌", `身份 ${key}`, () => openRotation(key)),
  ];
  const isActor = key === snapshot.actor;
  if (!isActor) buttons.push(
    actionButton("删除", `身份 ${key}`, () => removeResource("principal", key, "该身份的令牌将失效，已发布的路由不会自动下线。"), true),
  );
  return record({
    title: key, type: principal.admin ? "管理员" : "受限部署身份", details,
    note: isActor ? "当前登录身份" : "", buttons,
    detailFallback: principal.admin ? "全部部署权限" : "未配置部署范围",
  });
}

function updateRouteMode() {
  const managed = $("route-mode").value === "managed";
  $("route-name").required = !managed;
  $("route-name-hint").hidden = !managed;
  for (const id of ["route-address", "route-start-file", "route-args", "route-auto-start", "route-port"]) {
    const enabled = id === "route-address" ? !managed : managed;
    $(`${id}-field`).hidden = !enabled;
    $(id).disabled = !enabled;
  }
}

function fillNodeSelect(select, value) {
  const nodes = entries(snapshot.nodes);
  const options = [new Option(nodes.length ? "请选择节点" : "暂无可用节点", "")];
  for (const [name, range] of nodes) options.push(new Option(`${name}（${portRange(range)}）`, name));
  if (value && !Object.hasOwn(snapshot.nodes || {}, value)) {
    const missing = new Option(`${value}（已移除，请重新选择）`, value);
    missing.disabled = true;
    options.push(missing);
  }
  select.replaceChildren(...options);
  select.value = value;
}

function closeEditor(type) {
  const form = $(`${type}-form`);
  form.hidden = true;
  delete form.dataset.editKey;
  form.reset();
  message(`${type}-error`);
  if (type === "route") updateDeployKeyActions();
  if (type === "route") {
    updateRouteMode();
  } else if (type === "principal") {
    $("node-grants").replaceChildren();
    updateScope();
  }
}

function openEditor(type, key = "", value = {}) {
  if (busy) return;
  if (type === "principal") closeRotation();
  const resource = resources[type];
  const form = $(`${type}-form`);
  closeEditor(type);
  form.dataset.editKey = key;
  if (type === "route") updateDeployKeyActions();
  const keyField = $(resource.key);
  keyField.value = key;
  keyField.readOnly = Boolean(key) && type !== "route";
  messageValue(`${type}-form-title`, `${key ? "编辑" : type === "route" ? "添加" : "新建"}${resource.label}`);
  if (type === "route") {
    messageValue("domain-hint", `完整域名直接使用；仅填名称（例如 a）时，自动补成 a.${snapshot.baseDomain}。`);
    $("route-name").value = value.name || "";
    $("route-description").value = value.description || "";
    $("route-mode").value = value.managed ? "managed" : "direct";
    $("route-address").value = value.managed ? "" : value.address || "";
    $("route-start-file").value = value.startFile || "";
    $("route-args").value = value.args?.length ? JSON.stringify(value.args) : "";
    $("route-auto-start").checked = value.autoStart === true;
    $("route-port").value = value.managed && value.port ? value.port : "";
    updateRouteMode();
  } else if (type === "node") {
    $("node-ip").value = value.ip || "";
    $("node-min").value = value.minPort ?? "";
    $("node-max").value = value.maxPort ?? "";
  } else {
    $("principal-role").value = value.admin ? "admin" : "scoped";
    $("principal-role").disabled = key === snapshot.actor;
    $("principal-aliases").value = (value.aliases || []).join("\n");
    $("principal-prefixes").value = (value.prefixes || []).join("\n");
    $("principal-domains").value = (value.domains || []).join("\n");
    $("principal-networks").value = (value.networks || []).join("\n");
    $("legacy-grants").hidden = !value.aliases?.length && !value.prefixes?.length && !entries(value.nodes).length;
    for (const [name, range] of entries(value.nodes)) appendGrant(name, range);
    updateScope();
  }
  form.hidden = false;
  if (type === "route") $("route-name").focus();
  else if (!key) keyField.focus();
  else {
    const target = type === "principal" ? $("principal-role") : form.querySelector("input:not([readonly])");
    (target.disabled ? form.querySelector("button[type=submit]") : target).focus();
  }
}

function updateScope() {
  const admin = $("principal-role").value === "admin";
  $("principal-scope").disabled = admin;
  $("principal-scope").hidden = admin;
  $("grant-empty").hidden = $("node-grants").childElementCount !== 0;
}

function appendGrant(name = "", range = {}) {
  const grant = $("grant-template").content.firstElementChild.cloneNode(true);
  const select = grant.querySelector(".grant-node");
  fillNodeSelect(select, name);
  grant.querySelector(".grant-min").value = range.minPort ?? "";
  grant.querySelector(".grant-max").value = range.maxPort ?? "";
  grant.querySelector(".remove-grant").addEventListener("click", () => {
    grant.remove();
    updateScope();
    $("add-grant").focus();
  });
  $("node-grants").append(grant);
  updateScope();
  return select;
}

function readGrantRows() {
  const grants = Object.create(null);
  for (const row of $("node-grants").children) {
    const node = row.querySelector(".grant-node").value;
    if (!Object.hasOwn(snapshot.nodes || {}, node)) throw new Error("请为每条授权选择已配置的节点。");
    if (Object.hasOwn(grants, node)) throw new Error(`节点「${node}」只能授权一次，请移除重复的节点授权。`);
    grants[node] = {
      minPort: row.querySelector(".grant-min").valueAsNumber,
      maxPort: row.querySelector(".grant-max").valueAsNumber,
    };
  }
  return grants;
}

function splitValues(value) {
  return value.split(/[\s,，、]+/u).filter(Boolean);
}

function payload(type) {
  if (type === "route") {
    const app = {
      domain: $("route-domain").value.trim(),
      name: $("route-name").value.trim(),
      description: $("route-description").value.trim(),
    };
    if ($("route-mode").value === "managed") {
      app.startFile = $("route-start-file").value;
      if (!app.startFile.startsWith("/")) throw new Error("启动文件须填写服务器上的绝对路径。");
      const args = $("route-args").value.trim();
      try {
        app.args = args ? JSON.parse(args) : [];
      } catch {
        throw new Error("启动参数须为有效的 JSON 字符串数组。");
      }
      if (!Array.isArray(app.args) || app.args.some((arg) => typeof arg !== "string")) {
        throw new Error("启动参数须为有效的 JSON 字符串数组。");
      }
      app.autoStart = $("route-auto-start").checked;
      const port = $("route-port");
      if (port.value !== "") app.port = port.valueAsNumber;
    } else {
      app.address = $("route-address").value.trim();
    }
    return app;
  }
  if (type === "node") return {
    ip: $("node-ip").value.trim(),
    minPort: $("node-min").valueAsNumber,
    maxPort: $("node-max").valueAsNumber,
  };
  if ($("principal-role").value === "admin") return { admin: true };
  return {
    admin: false,
    aliases: splitValues($("principal-aliases").value),
    prefixes: splitValues($("principal-prefixes").value),
    domains: splitValues($("principal-domains").value.toLowerCase()),
    networks: splitValues($("principal-networks").value),
    nodes: readGrantRows(),
  };
}

function updateDeployKeyActions() {
  $("deploy-key-actions").hidden = snapshot?.apiMode === "off" || Boolean($("route-form").dataset.editKey);
}

function showToken(name, token, deployment) {
  const receipt = element("section", "receipt");
  const heading = element("h2", "", deployment ? `临时 API Key · ${name}` : `新令牌 · ${name}`);
  const help = element("p", "", deployment
    ? `仅授权添加此应用，成功使用后失效，有效至 ${new Date(deployment.expiresAt).toLocaleString()}。请将密钥单独交给 AI 的凭据配置；关闭后不能再查看。`
    : "请将令牌保存到安全位置。系统只显示一次，关闭后不能再查看。");
  const value = element("textarea");
  value.rows = 2;
  value.readOnly = true;
  value.spellcheck = false;
  value.autocomplete = "off";
  value.setAttribute("aria-label", deployment ? `${name} 的临时 API Key` : `${name} 的新令牌`);
  value.value = token;
  const actions = element("div", "actions");
  const copy = element("button", "primary", deployment ? "复制临时密钥" : "复制令牌");
  const dismiss = element("button", "quiet", "关闭回执");
  const status = element("span", "muted");
  status.setAttribute("role", "status");
  copy.type = dismiss.type = "button";
  copy.addEventListener("click", async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error();
      await navigator.clipboard.writeText(value.value);
      status.textContent = "已复制。";
    } catch {
      if (!receipt.isConnected) return;
      value.focus();
      value.select();
      status.textContent = "无法自动复制，已选中令牌，请手动复制。";
    }
  });
  dismiss.addEventListener("click", () => {
    value.value = "";
    receipt.remove();
    $("token-receipts").hidden = $("token-receipts").childElementCount === 0;
    $("refresh").focus();
  });
  actions.append(copy, dismiss, status);
  receipt.append(heading, help);
  if (deployment) {
    const endpoint = `${location.origin}${apiPath}/routes`;
    const requestInfo = element("div", "receipt-request");
    requestInfo.append(element("code", "", `POST ${endpoint}`), element("code", "", JSON.stringify({ domain: deployment.domain })));
    receipt.append(requestInfo);
  }
  receipt.append(value, actions);
  $("token-receipts").prepend(receipt);
  $("token-receipts").hidden = false;
  value.focus();
}

async function refresh(current, success = "", guidance = "") {
  try {
    const data = await request(current, "data");
    if (session !== current) return;
    refreshView(data);
    message("activity", success);
  } catch (error) {
    if (session !== current) return;
    if (rejectedSession(error)) {
      resetSession([success, error.message, error.status === 403 && guidance].filter(Boolean).join(" "));
    } else {
      message("activity", success);
      message("workspace-error", `数据刷新失败：${error.message}`);
    }
  }
}

async function mutate(path, method, body, { errorID, success, commit, guidance, pending = "正在保存更改…" }) {
  if (busy || !session) return;
  const current = session;
  setBusy(true);
  message("activity", pending);
  message("workspace-error");
  message(errorID);
  try {
    const result = await request(current, path, method, body);
    if (session !== current) return;
    commit?.(result);
    await refresh(current, success, guidance);
  } catch (error) {
    if (session !== current) return;
    if (error.status === 401) {
      resetSession(error.message);
      return;
    }
    if (error.status === 403) {
      // Distinguish losing access to the console from a denied operation.
      try {
        await request(current, "data");
      } catch (check) {
        if (session !== current) return;
        if (rejectedSession(check)) {
          resetSession(check.message === error.message ? error.message : `${error.message} ${check.message}`);
          return;
        }
      }
      if (session !== current) return;
    }
    message("activity");
    message(errorID, error.message);
  } finally {
    if (session === current) setBusy(false);
  }
}

function resourcePath(type, key) {
  return `${resources[type].path}/${encodeURIComponent(key)}`;
}

function setRouteRunning(key, running) {
  const action = running ? "启动" : "停止";
  return mutate(`${resourcePath("route", key)}/${running ? "start" : "stop"}`, "POST", {}, {
    errorID: "workspace-error",
    pending: `正在${action}应用「${key}」…`,
    success: `已${action}应用「${key}」。`,
  });
}

function removeResource(type, key, consequence) {
  const { label } = resources[type];
  if (!confirm(`确定删除${label}「${key}」吗？${consequence}`)) return;
  return mutate(resourcePath(type, key), "DELETE", undefined, {
    errorID: "workspace-error",
    success: `已删除${label}「${key}」。`,
    commit: () => {
      if ($(`${type}-form`).dataset.editKey === key) closeEditor(type);
    },
  });
}

function closeRotation() {
  rotationTarget = "";
  $("rotate-form").reset();
  $("rotate-form").hidden = true;
  message("rotate-error");
}

function openRotation(key) {
  if (busy) return;
  closeEditor("principal");
  closeRotation();
  rotationTarget = key;
  messageValue("rotate-title", `修改令牌 · ${key}`);
  $("rotate-form").hidden = false;
  $("new-token").focus();
}

$("rotate-cancel").addEventListener("click", () => {
  const button = Array.from($("principal-list").querySelectorAll("button"))
    .find((item) => item.getAttribute("aria-label") === `修改令牌 身份 ${rotationTarget}`);
  closeRotation();
  (button || $("new-principal")).focus();
});
$("rotate-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (busy || !session || !rotationTarget) return;
  const key = rotationTarget;
  const token = $("new-token").value;
  if (!/^[A-Za-z0-9._~+/=-]{16,128}$/u.test(token)) {
    message("rotate-error", "请按要求填写 16–128 个允许的字符。");
    $("new-token").focus();
    return;
  }
  if (token !== $("confirm-token").value) {
    message("rotate-error", "两次输入的令牌不一致。");
    $("confirm-token").focus();
    return;
  }
  await mutate(`${resourcePath("principal", key)}/rotate`, "POST", { token }, {
    errorID: "rotate-error",
    success: `已修改「${key}」的令牌。`,
    commit: () => {
      if (key === snapshot.actor) session.token = token;
      closeRotation();
    },
  });
  if ($("rotate-form").hidden && session) $("new-principal").focus();
});

$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (busy) return;
  const token = loginInput.value.trim();
  loginInput.value = "";
  if (!token) {
    message("login-error", "请输入管理员的 Bearer 令牌。");
    loginInput.focus();
    return;
  }
  const current = { token, controller: new AbortController() };
  session = current;
  setBusy(true);
  message("login-error");
  try {
    const data = await request(current, "data");
    if (session !== current) return;
    refreshView(data);
    $("signin").hidden = true;
    $("workspace").hidden = false;
    $("logout").hidden = false;
    setBusy(false);
    sectionButtons[0].focus();
  } catch (error) {
    if (session === current) resetSession(error.message);
  }
});

$("logout").addEventListener("click", () => resetSession());
window.addEventListener("pagehide", () => resetSession("", false));
$("refresh").addEventListener("click", async () => {
  if (busy || !session) return;
  const current = session;
  setBusy(true);
  message("workspace-error");
  message("activity", "正在刷新数据…");
  await refresh(current, "数据已刷新。");
  if (session === current) {
    setBusy(false);
    $("refresh").focus();
  }
});
for (const button of sectionButtons) {
  button.addEventListener("click", () => switchSection(button.dataset.section));
}
for (const [type, { key: keyID, label }] of Object.entries(resources)) {
  const form = $(`${type}-form`);
  const newButton = $(`new-${type}`);
  newButton.addEventListener("click", () => openEditor(type));
  form.querySelector("[data-cancel]").addEventListener("click", () => {
    closeEditor(type);
    newButton.focus();
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    if (busy || !session) return;
    const key = $(keyID).value.trim();
    const original = form.dataset.editKey;
    const create = !original;
    let body;
    try {
      body = payload(type);
      if (type !== "route") body.create = create;
    } catch (error) {
      message(`${type}-error`, error.message);
      return;
    }
    if (type === "route" && create && event.submitter?.value === "issue-key") {
      return mutate("deploy-keys", "POST", body, {
        errorID: "route-error",
        pending: "正在签发临时密钥…",
        success: "临时 API Key 已生成；应用尚未添加。",
        commit: (result) => {
          closeEditor(type);
          showToken(result.domain, result.token, result);
        },
      });
    }
    const path = type === "route" && create ? resources.route.path : resourcePath(type, original || key);
    return mutate(path, type === "route" && create ? "POST" : "PUT", body, {
      errorID: `${type}-error`,
      success: `已${create ? "新建" : "更新"}${label}「${key}」。`,
      commit: (result) => {
        closeEditor(type);
        if (type === "principal" && create) showToken(key, result.token);
      },
    });
  });
}
$("route-mode").addEventListener("change", updateRouteMode);
$("principal-role").addEventListener("change", updateScope);
$("add-grant").addEventListener("click", () => appendGrant().focus());
$("api-mode").addEventListener("change", () => {
  settingsDirty = $("api-mode").value !== snapshot.apiMode;
  updateModeHint();
});
$("settings-form").addEventListener("submit", (event) => {
  event.preventDefault();
  if (busy || !session) return;
  const apiMode = $("api-mode").value;
  return mutate("settings", "PUT", { apiMode }, {
    errorID: "settings-error",
    success: `已保存部署 API 设置：${modeNames[apiMode]}。`,
    guidance: apiMode !== "on" ? ipGuidance(snapshot.listenPort) : "",
    commit: () => { settingsDirty = false; },
  });
});
$("mcp-path").addEventListener("input", () => {
  mcpPathDirty = $("mcp-path").value !== snapshot.mcpPath;
});
$("mcp-settings-form").addEventListener("submit", (event) => {
  event.preventDefault();
  if (busy || !session) return;
  const mcpPath = $("mcp-path").value;
  return mutate("settings/mcp", "PUT", { mcpPath }, {
    errorID: "mcp-settings-error",
    success: `已保存 MCP 路径：${mcpPath}。请更新客户端地址。`,
    commit: () => { mcpPathDirty = false; },
  });
});
setBusy(false);
